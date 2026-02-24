package main

import (
	"strings"
	"time"
)

func (mc *MinerConn) isBanned(now time.Time) bool {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	return now.Before(mc.ban.banUntil)
}

func (mc *MinerConn) banDetails() (time.Time, string, int) {
	mc.stateMu.Lock()
	defer mc.stateMu.Unlock()
	return mc.ban.banUntil, mc.ban.banReason, mc.ban.invalidSubs
}

func (mc *MinerConn) logBan(reason, worker string, invalidSubs int) {
	until, banReason, _ := mc.banDetails()
	if banReason == "" {
		banReason = reason
	}
	if mc.accounting != nil && worker != "" {
		mc.accounting.MarkBan(worker, until, banReason)
	}
	logger.Warn("miner banned",
		"miner", mc.minerName(worker),
		"remote", mc.id,
		"reason", banReason,
		"ban_until", until,
		"invalid_submissions", invalidSubs,
	)
}

func (mc *MinerConn) adminBan(reason string, duration time.Duration) {
	if mc == nil {
		return
	}
	if duration <= 0 {
		duration = defaultBanInvalidSubmissionsDuration
	}
	if reason == "" {
		reason = "admin ban"
	}
	now := time.Now()
	mc.stateMu.Lock()
	mc.ban.banUntil = now.Add(duration)
	mc.ban.banReason = reason
	mc.stateMu.Unlock()
	mc.logBan(reason, mc.currentWorker(), 0)
	mc.sendClientShowMessage("Banned: " + reason)
}

func (mc *MinerConn) banFor(reason string, duration time.Duration, worker string) {
	if mc == nil {
		return
	}
	if duration <= 0 {
		duration = defaultBanInvalidSubmissionsDuration
	}
	if reason == "" {
		reason = "ban"
	}
	now := time.Now()
	mc.stateMu.Lock()
	mc.ban.banUntil = now.Add(duration)
	mc.ban.banReason = reason
	mc.stateMu.Unlock()
	mc.logBan(reason, worker, 0)
	mc.sendClientShowMessage("Banned: " + reason)
}

func (mc *MinerConn) lookupPersistedBan(worker string) (WorkerView, bool) {
	if mc == nil || mc.accounting == nil {
		return WorkerView{}, false
	}
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return WorkerView{}, false
	}
	if view, ok := mc.accounting.WorkerViewByName(worker); ok && view.Banned {
		return view, true
	}
	wallet := workerBaseAddress(worker)
	if wallet != "" && wallet != worker {
		if view, ok := mc.accounting.WorkerViewByName(wallet); ok && view.Banned {
			return view, true
		}
	}
	return WorkerView{}, false
}
