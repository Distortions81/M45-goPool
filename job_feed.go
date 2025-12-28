package main

import (
	"context"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"syscall"

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
}

func (jm *JobManager) markZMQUnhealthy(reason string, err error) {
	if jm.cfg.ZMQBlockAddr == "" {
		return
	}
	atomic.AddUint64(&jm.zmqDisconnects, 1)
	fields := []interface{}{"reason", reason}
	if err != nil {
		fields = append(fields, "error", err)
	}
	if jm.zmqHealthy.Swap(false) {
		logger.Warn("zmq watcher unhealthy", fields...)
	} else if err != nil {
		logger.Error("zmq watcher error", fields...)
	}
}

func (jm *JobManager) shouldUseLongpollFallback() bool {
	return jm.cfg.ZMQBlockAddr == "" || jm.cfg.ZMQLongpollFallback
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
				if err := sleepContext(ctx, jobRetryDelay); err != nil {
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
			if err := sleepContext(ctx, jobRetryDelay); err != nil {
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
			if err := sleepContext(ctx, jobRetryDelay); err != nil {
				return
			}
			continue
		}
		if !jm.shouldUseLongpollFallback() {
			continue
		}

		if err := jm.refreshFromTemplate(ctx, tpl); err != nil {
			logger.Error("longpoll refresh error", "error", err)
			if errors.Is(err, errStaleTemplate) {
				if err := jm.refreshJobCtx(ctx); err != nil {
					logger.Error("fallback refresh after stale template", "error", err)
				}
			}
			if err := sleepContext(ctx, jobRetryDelay); err != nil {
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
		jm.markZMQHealthy()
		return jm.refreshJobCtx(ctx)
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
		return jm.refreshJobCtx(ctx)
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

// Prefer block notifications when bitcoind is configured with -zmqpubhashblock (docs/protocols/zmq.md).
func (jm *JobManager) zmqBlockLoop(ctx context.Context) {
zmqLoop:
	for {
		if ctx.Err() != nil {
			return
		}
		if jm.CurrentJob() == nil {
			if err := jm.refreshJobCtx(ctx); err != nil {
				logger.Error("zmq loop refresh (no job) error", "error", err)
				if err := sleepContext(ctx, jobRetryDelay); err != nil {
					return
				}
				continue
			}
		}

		sub, err := zmq4.NewSocket(zmq4.SUB)
		if err != nil {
			jm.markZMQUnhealthy("socket", err)
			if err := sleepContext(ctx, jobRetryDelay); err != nil {
				return
			}
			continue
		}

		topics := []string{"hashblock", "rawblock", "hashtx", "rawtx"}
		for _, topic := range topics {
			if err := sub.SetSubscribe(topic); err != nil {
				jm.markZMQUnhealthy("subscribe", err)
				sub.Close()
				if err := sleepContext(ctx, jobRetryDelay); err != nil {
					return
				}
				continue zmqLoop
			}
		}

		if err := sub.SetRcvtimeo(defaultZMQReceiveTimeout); err != nil {
			jm.markZMQUnhealthy("set_rcvtimeo", err)
			sub.Close()
			if err := sleepContext(ctx, jobRetryDelay); err != nil {
				return
			}
			continue
		}

		if err := sub.Connect(jm.cfg.ZMQBlockAddr); err != nil {
			jm.markZMQUnhealthy("connect", err)
			sub.Close()
			if err := sleepContext(ctx, jobRetryDelay); err != nil {
				return
			}
			continue
		}

		jm.markZMQHealthy()
		logger.Info("watching ZMQ block notifications", "addr", jm.cfg.ZMQBlockAddr)

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
				if err := sleepContext(ctx, jobRetryDelay); err != nil {
					return
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
				if err := sleepContext(ctx, jobRetryDelay); err != nil {
					sub.Close()
					return
				}
			}
		}
	}
}
