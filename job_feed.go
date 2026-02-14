package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pebbe/zmq4"
)

func (jm *JobManager) zmqEnabled() bool {
	return jm.cfg.ZMQHashBlockAddr != "" || jm.cfg.ZMQRawBlockAddr != ""
}

func (jm *JobManager) zmqAnyHealthy() bool {
	return jm.zmqHashblockHealthy.Load() || jm.zmqRawblockHealthy.Load()
}

func (jm *JobManager) zmqTopicsHealthy(topics []string) bool {
	if len(topics) == 0 {
		return false
	}
	for _, topic := range topics {
		switch topic {
		case "hashblock":
			if !jm.zmqHashblockHealthy.Load() {
				return false
			}
		case "rawblock":
			if !jm.zmqRawblockHealthy.Load() {
				return false
			}
		}
	}
	return true
}

func (jm *JobManager) markZMQHealthy(topics []string, addr string) {
	if !jm.zmqEnabled() {
		return
	}
	prevAny := jm.zmqAnyHealthy()
	for _, topic := range topics {
		switch topic {
		case "hashblock":
			jm.zmqHashblockHealthy.Store(true)
		case "rawblock":
			jm.zmqRawblockHealthy.Store(true)
		}
	}
	if prevAny || !jm.zmqAnyHealthy() {
		return
	}

	logger.Info("zmq watcher healthy", "addr", addr, "topics", topics)
	atomic.AddUint64(&jm.zmqReconnects, 1)
	if jm.metrics != nil {
		verb := "connected"
		if atomic.LoadUint64(&jm.zmqDisconnects) > 0 {
			verb = "reconnected"
		}
		jm.metrics.RecordErrorEvent("zmq", verb+" to "+addr, time.Now())
	}
	verb := "connected"
	if atomic.LoadUint64(&jm.zmqDisconnects) > 0 {
		verb = "reconnected"
	}
	jm.lastErrMu.Lock()
	jm.appendJobFeedError("event: zmq " + verb + " (" + addr + ")")
	jm.lastErrMu.Unlock()
}

func (jm *JobManager) markZMQUnhealthy(topics []string, addr string, reason string, err error) {
	if !jm.zmqEnabled() {
		return
	}
	prevAny := jm.zmqAnyHealthy()
	for _, topic := range topics {
		switch topic {
		case "hashblock":
			jm.zmqHashblockHealthy.Store(false)
		case "rawblock":
			jm.zmqRawblockHealthy.Store(false)
		}
	}

	fields := []interface{}{"reason", reason, "addr", addr, "topics", topics}
	if err != nil {
		fields = append(fields, "error", err)
	}
	if prevAny && !jm.zmqAnyHealthy() {
		atomic.AddUint64(&jm.zmqDisconnects, 1)
		logger.Warn("zmq watcher unhealthy", fields...)
	} else if err != nil {
		logger.Error("zmq watcher error", fields...)
	}
}

func (jm *JobManager) longpollLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job := jm.CurrentJob()
		if job == nil {
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("longpoll refresh (no job) error", "error", err)
				if err := jm.sleepRetry(ctx); err != nil {
					return
				}
				continue
			}
			continue
		}

		if job.Template.LongPollID == "" {
			logger.Warn("longpollid missing; refreshing job normally")
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("job refresh error", "error", err)
			}
			if err := jm.sleepRetry(ctx); err != nil {
				return
			}
			continue
		}

		params := map[string]interface{}{
			"rules":      []string{"segwit"},
			"longpollid": job.Template.LongPollID,
		}
		tpl, err := jm.fetchTemplateCtx(ctx, params, true)
		if err != nil {
			jm.recordJobError(err)
			if errors.Is(err, context.Canceled) {
				// bitcoind can cancel longpoll waits during template churn; treat
				// this as a transient condition unless our own context is done.
				if ctx.Err() != nil {
					return
				}
				logger.Warn("longpoll gbt canceled; retrying", "error", err)
			} else {
				logger.Error("longpoll gbt error", "error", err)
			}
			if err := jm.sleepRetry(ctx); err != nil {
				return
			}
			continue
		}

		if err := jm.refreshFromTemplate(ctx, tpl); err != nil {
			logger.Error("longpoll refresh error", "error", err)
			if errors.Is(err, errStaleTemplate) {
				if err := jm.refreshJobCtx(ctx); err != nil {
					logger.Error("fallback refresh after stale template", "error", err)
				}
			}
			if err := jm.sleepRetry(ctx); err != nil {
				return
			}
			continue
		}
	}
}

func (jm *JobManager) handleZMQNotification(ctx context.Context, topic string, payload []byte) error {
	switch topic {
	case "hashblock":
		blockHash := hex.EncodeToString(payload)
		logger.Info("zmq block notification", "block_hash", blockHash)
		return jm.refreshJobCtxForce(ctx)
	case "rawblock":
		tip, err := parseRawBlockTip(payload)
		if err != nil {
			if debugLogging {
				logger.Debug("parse raw block tip failed", "error", err)
			}
		} else {
			jm.recordBlockTip(tip)
		}
		jm.recordRawBlockPayload(len(payload))
		// Some deployments only publish rawblock and not hashblock; refresh the
		// template on rawblock as well so job/tip advance on new blocks.
		return jm.refreshJobCtxForce(ctx)
	default:
		return nil
	}
}

func (jm *JobManager) startZMQMonitor(ctx context.Context, sub *zmq4.Socket, remoteAddr string, topics []string) (*zmq4.Socket, error) {
	// inproc address must be unique per socket.
	inprocAddr := fmt.Sprintf("inproc://gopool.zmq.sub.monitor.%d", time.Now().UnixNano())

	events := zmq4.EVENT_CONNECTED | zmq4.EVENT_CONNECT_DELAYED | zmq4.EVENT_CONNECT_RETRIED | zmq4.EVENT_DISCONNECTED | zmq4.EVENT_CLOSED | zmq4.EVENT_MONITOR_STOPPED
	if err := sub.Monitor(inprocAddr, events); err != nil {
		return nil, err
	}

	mon, err := zmq4.NewSocket(zmq4.PAIR)
	if err != nil {
		return nil, err
	}
	_ = mon.SetLinger(0)
	_ = mon.SetRcvtimeo(time.Second)
	if err := mon.Connect(inprocAddr); err != nil {
		mon.Close()
		return nil, err
	}

	go func() {
		defer mon.Close()
		for {
			if ctx.Err() != nil {
				return
			}
			ev, evAddr, _, err := mon.RecvEvent(0)
			if err != nil {
				eno := zmq4.AsErrno(err)
				if eno == zmq4.Errno(syscall.EAGAIN) || eno == zmq4.ETIMEDOUT {
					continue
				}
				// Monitor socket errors typically mean the monitored socket closed.
				jm.markZMQUnhealthy(topics, remoteAddr, "monitor_receive", err)
				return
			}

			switch ev {
			case zmq4.EVENT_CONNECTED:
				wasTopicsHealthy := jm.zmqTopicsHealthy(topics)
				jm.markZMQHealthy(topics, remoteAddr)
				// Only force a template refresh when the watched topics actually
				// transition from unhealthy to healthy.
				if !wasTopicsHealthy {
					if err := jm.refreshJobCtxForce(ctx); err != nil {
						logger.Warn("refresh after zmq monitor connect failed", "error", err)
					}
				}
			case zmq4.EVENT_DISCONNECTED, zmq4.EVENT_CLOSED, zmq4.EVENT_MONITOR_STOPPED:
				jm.markZMQUnhealthy(topics, remoteAddr, "monitor_"+ev.String(), nil)
			case zmq4.EVENT_CONNECT_DELAYED, zmq4.EVENT_CONNECT_RETRIED:
				// CONNECT_DELAYED/RETRIED can appear transiently while a socket is
				// still healthy; don't downgrade health on those events alone.
				if debugLogging {
					logger.Debug("zmq monitor reconnect in progress", "event", ev.String(), "addr", remoteAddr, "topics", topics)
				}
			default:
				_ = evAddr
			}
		}
	}()

	return mon, nil
}

func (jm *JobManager) startZMQLoops(ctx context.Context) {
	type topicSpec struct {
		name string
		addr string
	}
	specs := []topicSpec{
		{name: "hashblock", addr: jm.cfg.ZMQHashBlockAddr},
		{name: "rawblock", addr: jm.cfg.ZMQRawBlockAddr},
	}

	addrTopics := make(map[string][]string)
	for _, spec := range specs {
		addr := spec.addr
		if addr == "" {
			continue
		}
		addrTopics[addr] = append(addrTopics[addr], spec.name)
	}

	for addr, topics := range addrTopics {
		label := strings.Join(topics, "+")
		go jm.zmqLoop(ctx, addr, topics, label)
	}
}

func (jm *JobManager) zmqLoop(ctx context.Context, addr string, topics []string, label string) {
	backoff := defaultZMQRecreateBackoffMin
zmqLoop:
	for {
		if ctx.Err() != nil {
			return
		}
		if jm.CurrentJob() == nil {
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("zmq loop refresh (no job) error", "error", err)
				if err := jm.sleepRetry(ctx); err != nil {
					return
				}
				continue
			}
		}

		sub, err := zmq4.NewSocket(zmq4.SUB)
		if err != nil {
			jm.markZMQUnhealthy(topics, addr, label+"_socket", err)
			if err := sleepContext(ctx, backoff); err != nil {
				return
			}
			if backoff < defaultZMQRecreateBackoffMax {
				backoff *= 2
				if backoff > defaultZMQRecreateBackoffMax {
					backoff = defaultZMQRecreateBackoffMax
				}
			}
			continue
		}
		_ = sub.SetLinger(0)

		for _, topic := range topics {
			if err := sub.SetSubscribe(topic); err != nil {
				jm.markZMQUnhealthy(topics, addr, label+"_subscribe", err)
				sub.Close()
				if err := sleepContext(ctx, backoff); err != nil {
					return
				}
				if backoff < defaultZMQRecreateBackoffMax {
					backoff *= 2
					if backoff > defaultZMQRecreateBackoffMax {
						backoff = defaultZMQRecreateBackoffMax
					}
				}
				continue zmqLoop
			}
		}

		if err := sub.SetRcvtimeo(defaultZMQReceiveTimeout); err != nil {
			jm.markZMQUnhealthy(topics, addr, label+"_set_rcvtimeo", err)
			sub.Close()
			if err := sleepContext(ctx, backoff); err != nil {
				return
			}
			if backoff < defaultZMQRecreateBackoffMax {
				backoff *= 2
				if backoff > defaultZMQRecreateBackoffMax {
					backoff = defaultZMQRecreateBackoffMax
				}
			}
			continue
		}

		if err := sub.SetConnectTimeout(defaultZMQConnectTimeout); err != nil && debugLogging {
			logger.Debug("zmq set connect timeout failed (ignored)", "error", err)
		}
		_ = sub.SetReconnectIvl(defaultZMQReconnectInterval)
		_ = sub.SetReconnectIvlMax(defaultZMQReconnectMax)
		_ = sub.SetHeartbeatIvl(defaultZMQHeartbeatInterval)
		_ = sub.SetHeartbeatTimeout(defaultZMQHeartbeatTimeout)
		_ = sub.SetHeartbeatTtl(defaultZMQHeartbeatTTL)

		mon, err := jm.startZMQMonitor(ctx, sub, addr, topics)
		if err != nil {
			jm.markZMQUnhealthy(topics, addr, label+"_monitor", err)
			sub.Close()
			if err := sleepContext(ctx, backoff); err != nil {
				return
			}
			if backoff < defaultZMQRecreateBackoffMax {
				backoff *= 2
				if backoff > defaultZMQRecreateBackoffMax {
					backoff = defaultZMQRecreateBackoffMax
				}
			}
			continue
		}
		_ = mon

		if err := sub.Connect(addr); err != nil {
			jm.markZMQUnhealthy(topics, addr, label+"_connect", err)
			sub.Close()
			if err := sleepContext(ctx, backoff); err != nil {
				return
			}
			if backoff < defaultZMQRecreateBackoffMax {
				backoff *= 2
				if backoff > defaultZMQRecreateBackoffMax {
					backoff = defaultZMQRecreateBackoffMax
				}
			}
			continue
		}

		jm.markZMQHealthy(topics, addr)
		logger.Info("watching ZMQ notifications", "addr", addr, "topics", topics, "label", label)
		backoff = defaultZMQRecreateBackoffMin

		for {
			if ctx.Err() != nil {
				jm.markZMQUnhealthy(topics, addr, label+"_context_done", nil)
				sub.Close()
				return
			}
			frames, err := sub.RecvMessageBytes(0)
			if err != nil {
				eno := zmq4.AsErrno(err)
				if eno == zmq4.Errno(syscall.EAGAIN) || eno == zmq4.ETIMEDOUT {
					continue
				}
				jm.markZMQUnhealthy(topics, addr, label+"_receive", err)
				sub.Close()
				if err := sleepContext(ctx, backoff); err != nil {
					return
				}
				if backoff < defaultZMQRecreateBackoffMax {
					backoff *= 2
					if backoff > defaultZMQRecreateBackoffMax {
						backoff = defaultZMQRecreateBackoffMax
					}
				}
				break
			}
			// Ensure we have at least topic and payload frames
			if len(frames) < 2 {
				logger.Warn("zmq notification malformed", "frames", len(frames))
				continue
			}

			topic := string(frames[0])
			payload := frames[1]
			jm.markZMQHealthy([]string{topic}, addr)
			if err := jm.handleZMQNotification(ctx, topic, payload); err != nil {
				logger.Error("refresh after zmq notification error", "topic", topic, "error", err)
				if err := sleepContext(ctx, backoff); err != nil {
					sub.Close()
					return
				}
				if backoff < defaultZMQRecreateBackoffMax {
					backoff *= 2
					if backoff > defaultZMQRecreateBackoffMax {
						backoff = defaultZMQRecreateBackoffMax
					}
				}
			}
		}
	}
}
