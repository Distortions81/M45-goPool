package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pebbe/zmq4"
)

func (jm *JobManager) markZMQHealthy() {
	if jm.cfg.ZMQBlockAddr == "" {
		return
	}
	if jm.zmqHealthy.Swap(true) {
		return
	}
	logger.Info("zmq watcher healthy", "addr", jm.cfg.ZMQBlockAddr)
	atomic.AddUint64(&jm.zmqReconnects, 1)
	if jm.metrics != nil {
		verb := "connected"
		if atomic.LoadUint64(&jm.zmqDisconnects) > 0 {
			verb = "reconnected"
		}
		jm.metrics.RecordErrorEvent("zmq", verb+" to "+jm.cfg.ZMQBlockAddr, time.Now())
	}
	verb := "connected"
	if atomic.LoadUint64(&jm.zmqDisconnects) > 0 {
		verb = "reconnected"
	}
	jm.lastErrMu.Lock()
	jm.appendJobFeedError("event: zmq " + verb + " (" + jm.cfg.ZMQBlockAddr + ")")
	jm.lastErrMu.Unlock()
}

func (jm *JobManager) markZMQUnhealthy(reason string, err error) {
	if jm.cfg.ZMQBlockAddr == "" {
		return
	}
	fields := []interface{}{"reason", reason}
	if err != nil {
		fields = append(fields, "error", err)
	}
	if jm.zmqHealthy.Swap(false) {
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
			logger.Error("longpoll gbt error", "error", err)
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
	// Any ZMQ message implies the socket is alive and we can consider it healthy
	// (even if the deployment only publishes a subset of topics).
	jm.markZMQHealthy()

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
	case "hashtx":
		txHash := hex.EncodeToString(payload)
		jm.recordHashTx(txHash)
		return nil
	case "rawtx":
		jm.recordRawTxPayload(len(payload))
		return nil
	default:
		return nil
	}
}

func (jm *JobManager) startZMQMonitor(ctx context.Context, sub *zmq4.Socket) (*zmq4.Socket, error) {
	// inproc address must be unique per socket.
	addr := fmt.Sprintf("inproc://gopool.zmq.sub.monitor.%d", time.Now().UnixNano())

	events := zmq4.EVENT_CONNECTED | zmq4.EVENT_CONNECT_DELAYED | zmq4.EVENT_CONNECT_RETRIED | zmq4.EVENT_DISCONNECTED | zmq4.EVENT_CLOSED | zmq4.EVENT_MONITOR_STOPPED
	if err := sub.Monitor(addr, events); err != nil {
		return nil, err
	}

	mon, err := zmq4.NewSocket(zmq4.PAIR)
	if err != nil {
		return nil, err
	}
	_ = mon.SetLinger(0)
	_ = mon.SetRcvtimeo(time.Second)
	if err := mon.Connect(addr); err != nil {
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
				jm.markZMQUnhealthy("monitor_receive", err)
				return
			}

			switch ev {
			case zmq4.EVENT_CONNECTED:
				jm.markZMQHealthy()
				if err := jm.refreshJobCtxForce(ctx); err != nil {
					logger.Warn("refresh after zmq monitor connect failed", "error", err)
				}
			case zmq4.EVENT_DISCONNECTED, zmq4.EVENT_CLOSED, zmq4.EVENT_MONITOR_STOPPED:
				jm.markZMQUnhealthy("monitor_"+ev.String(), nil)
			case zmq4.EVENT_CONNECT_DELAYED, zmq4.EVENT_CONNECT_RETRIED:
				jm.markZMQUnhealthy("monitor_"+ev.String(), nil)
			default:
				_ = evAddr
			}
		}
	}()

	return mon, nil
}

// Prefer block notifications when bitcoind is configured with -zmqpubhashblock (docs/protocols/zmq.md).
func (jm *JobManager) zmqBlockLoop(ctx context.Context) {
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
			jm.markZMQUnhealthy("socket", err)
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

		topics := []string{"hashblock", "rawblock", "hashtx", "rawtx"}
		for _, topic := range topics {
			if err := sub.SetSubscribe(topic); err != nil {
				jm.markZMQUnhealthy("subscribe", err)
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
			jm.markZMQUnhealthy("set_rcvtimeo", err)
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

		mon, err := jm.startZMQMonitor(ctx, sub)
		if err != nil {
			jm.markZMQUnhealthy("monitor", err)
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

		if err := sub.Connect(jm.cfg.ZMQBlockAddr); err != nil {
			jm.markZMQUnhealthy("connect", err)
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

		jm.markZMQHealthy()
		logger.Info("watching ZMQ block notifications", "addr", jm.cfg.ZMQBlockAddr)
		backoff = defaultZMQRecreateBackoffMin

		for {
			if ctx.Err() != nil {
				sub.Close()
				return
			}
			frames, err := sub.RecvMessageBytes(0)
			if err != nil {
				eno := zmq4.AsErrno(err)
				if eno == zmq4.Errno(syscall.EAGAIN) || eno == zmq4.ETIMEDOUT {
					continue
				}
				jm.markZMQUnhealthy("receive", err)
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
