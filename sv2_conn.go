package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// sv2Conn is a minimal submit-path skeleton for the future SV2 connection
// handler. It currently supports reading framed submit messages, mapping them
// into the shared submit core, and writing submit success/error responses.
type sv2Conn struct {
	mc              *MinerConn
	reader          io.Reader
	writer          io.Writer
	transport       sv2FrameTransport
	writeMu         sync.Mutex
	stateMu         sync.RWMutex
	submitMapper    *stratumV2SubmitMapperState
	channelTargets  map[uint32][32]byte
	channelPrevHash map[uint32]stratumV2WireSetNewPrevHash
	nextChannelID   uint32
	nextWireJobID   uint32
	setupDone       bool
	setupVersion    uint16
	setupFlags      uint32
}

func newSV2ConnForMiner(mc *MinerConn, reader io.Reader, writer io.Writer) *sv2Conn {
	c := &sv2Conn{
		mc:           mc,
		reader:       reader,
		writer:       writer,
		transport:    &sv2PlainFrameTransport{r: reader, w: writer},
		submitMapper: newStratumV2SubmitMapperState(),
	}
	if mc != nil {
		mc.attachSV2Conn(c)
	}
	return c
}

func (c *sv2Conn) handleReadLoop() error {
	for {
		if err := c.handleOneFrame(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (c *sv2Conn) handleOneFrame() error {
	frameBytes, err := c.readFrame()
	if err != nil {
		return err
	}
	wireMsg, err := decodeStratumV2MiningWireFrame(frameBytes)
	if err != nil {
		return err
	}

	switch msg := wireMsg.(type) {
	case stratumV2WireSetupConnection:
		return c.handleSetupConnection(msg)
	case stratumV2WireOpenStandardMiningChannel:
		return c.handleOpenStandardMiningChannel(msg)
	case stratumV2WireOpenExtendedMiningChannel:
		return c.handleOpenExtendedMiningChannel(msg)
	case stratumV2WireSetExtranoncePrefix:
		c.applyStratumV2SetExtranoncePrefix(msg)
		return nil
	case stratumV2WireSetNewPrevHash:
		c.applyStratumV2SetNewPrevHash(msg)
		return nil
	case stratumV2WireNewMiningJob:
		if localJobID, ok := c.defaultLocalJobIDForWireNewMiningJob(msg); ok {
			c.applyStratumV2NewMiningJob(msg, localJobID)
		}
		return nil
	case stratumV2WireSubmitSharesStandard:
		return c.handleSubmitSharesStandard(msg)
	case stratumV2WireSubmitSharesExtended:
		return c.handleSubmitSharesExtended(msg)
	default:
		// Success/Error and other server-originated messages are outbound in this
		// skeleton; ignore if received.
		return nil
	}
}

func (c *sv2Conn) readFrame() ([]byte, error) {
	if c == nil {
		return nil, io.EOF
	}
	if c.transport == nil {
		c.transport = &sv2PlainFrameTransport{r: c.reader, w: c.writer}
	}
	return c.transport.ReadFrame()
}

func (c *sv2Conn) writeFrame(frame []byte) error {
	if c == nil {
		return io.ErrClosedPipe
	}
	if c.transport == nil {
		c.transport = &sv2PlainFrameTransport{r: c.reader, w: c.writer}
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.transport.WriteFrame(frame)
}

func (c *sv2Conn) handleSetupConnection(msg stratumV2WireSetupConnection) error {
	const (
		sv2ProtocolMining = uint8(0)
		sv2VersionCurrent = uint16(2)
	)
	if msg.Protocol != sv2ProtocolMining {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "unsupported-protocol",
		})
	}
	if msg.MinVersion > sv2VersionCurrent || msg.MaxVersion < sv2VersionCurrent {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "protocol-version-mismatch",
		})
	}
	c.stateMu.Lock()
	c.setupDone = true
	c.setupVersion = sv2VersionCurrent
	c.setupFlags = msg.Flags
	c.stateMu.Unlock()
	return c.writeSetupConnectionSuccess(stratumV2WireSetupConnectionSuccess{
		UsedVersion: sv2VersionCurrent,
		Flags:       0,
	})
}

func (c *sv2Conn) handleOpenStandardMiningChannel(msg stratumV2WireOpenStandardMiningChannel) error {
	c.stateMu.RLock()
	setupDone := c.setupDone
	c.stateMu.RUnlock()
	if !setupDone {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "setup-connection-required",
		})
	}
	if c.mc == nil {
		return fmt.Errorf("sv2 conn missing miner context")
	}
	if err := c.prepareSV2ChannelWorker(msg.UserIdentity); err != nil {
		return err
	}
	chID := c.allocateChannelID()
	en2Size := c.mc.cfg.Extranonce2Size
	if en2Size <= 0 {
		en2Size = 4
	}
	c.stateMu.Lock()
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	c.submitMapper.registerChannel(chID, stratumV2SubmitChannelMapping{
		WorkerName:          msg.UserIdentity,
		StandardExtranonce2: make([]byte, en2Size),
	})
	c.stateMu.Unlock()
	resp := stratumV2WireOpenStandardMiningChannelSuccess{
		RequestID:      msg.RequestID,
		ChannelID:      chID,
		Target:         c.currentSV2TargetBytes(),
		GroupChannelID: chID,
	}
	frame, err := encodeStratumV2OpenStandardMiningChannelSuccessFrame(resp)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	return c.writeCurrentJobBundleForChannel(chID)
}

func (c *sv2Conn) handleOpenExtendedMiningChannel(msg stratumV2WireOpenExtendedMiningChannel) error {
	c.stateMu.RLock()
	setupDone := c.setupDone
	c.stateMu.RUnlock()
	if !setupDone {
		return c.writeSetupConnectionError(stratumV2WireSetupConnectionError{
			Flags:     0,
			ErrorCode: "setup-connection-required",
		})
	}
	if c.mc == nil {
		return fmt.Errorf("sv2 conn missing miner context")
	}
	if err := c.prepareSV2ChannelWorker(msg.UserIdentity); err != nil {
		return err
	}
	chID := c.allocateChannelID()
	c.stateMu.Lock()
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	c.submitMapper.registerChannel(chID, stratumV2SubmitChannelMapping{
		WorkerName: msg.UserIdentity,
	})
	c.stateMu.Unlock()
	en2Size := uint16(c.mc.cfg.Extranonce2Size)
	if en2Size == 0 {
		en2Size = 4
	}
	if msg.MinExtranonceSize > 0 && msg.MinExtranonceSize > en2Size {
		en2Size = msg.MinExtranonceSize
	}
	resp := stratumV2WireOpenExtendedMiningChannelSuccess{
		RequestID:      msg.RequestID,
		ChannelID:      chID,
		Target:         c.currentSV2TargetBytes(),
		ExtranonceSize: en2Size,
		GroupChannelID: chID,
	}
	frame, err := encodeStratumV2OpenExtendedMiningChannelSuccessFrame(resp)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	return c.writeCurrentJobBundleForChannel(chID)
}

// prepareSV2ChannelWorker mirrors the minimum v1 authorize side effects needed
// by the shared submit parser/core, but without writing any v1 protocol frames.
func (c *sv2Conn) prepareSV2ChannelWorker(userIdentity string) error {
	if c == nil || c.mc == nil {
		return fmt.Errorf("sv2 conn missing miner context")
	}
	worker := strings.TrimSpace(userIdentity)
	if worker == "" {
		return fmt.Errorf("sv2 open channel missing user identity")
	}
	if len(worker) > maxWorkerNameLen {
		return fmt.Errorf("sv2 open channel worker name too long")
	}
	workerName := c.mc.updateWorker(worker)
	if workerName != "" {
		if _, _, ok := c.mc.ensureWorkerWallet(workerName); !ok {
			return fmt.Errorf("sv2 worker wallet validation failed")
		}
		c.mc.assignConnectionSeq()
		c.mc.registerWorker(workerName)
	}
	c.mc.stratumV1.authorized = true
	c.mc.stratumV1.subscribed = true
	return nil
}

func (c *sv2Conn) handleSubmitSharesStandard(msg stratumV2WireSubmitSharesStandard) error {
	if c.mc == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	c.stateMu.RLock()
	mapper := c.submitMapper
	var (
		norm stratumV2NormalizedSubmitShare
		err  error
	)
	if mapper != nil {
		norm, err = mapper.mapWireSubmitSharesStandard(msg)
	}
	c.stateMu.RUnlock()
	if mapper == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	responder := &stratumV2SubmitWireResponder{mc: c.mc, w: c.writer, writeFrame: c.writeFrame, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
	if !c.isActiveSV2SubmitJob(msg.ChannelID, msg.JobID) {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, "stale-share")
		return responder.err
	}
	if err != nil {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, c.classifySV2SubmitMappingError(msg.ChannelID, msg.JobID, err))
		return responder.err
	}
	task, ok := c.mc.prepareStratumV2SubmissionTask(norm, responder, time.Now())
	if !ok {
		// Current bridge may emit errors before hooks attach on parse failures.
		return responder.err
	}
	c.mc.processSubmissionTask(task)
	return responder.err
}

func (c *sv2Conn) handleSubmitSharesExtended(msg stratumV2WireSubmitSharesExtended) error {
	if c.mc == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	c.stateMu.RLock()
	mapper := c.submitMapper
	var (
		norm stratumV2NormalizedSubmitShare
		err  error
	)
	if mapper != nil {
		norm, err = mapper.mapWireSubmitSharesExtended(msg)
	}
	c.stateMu.RUnlock()
	if mapper == nil {
		return fmt.Errorf("sv2 submit skeleton not initialized")
	}
	responder := &stratumV2SubmitWireResponder{mc: c.mc, w: c.writer, writeFrame: c.writeFrame, channelID: msg.ChannelID, sequenceNumber: msg.SequenceNumber}
	if !c.isActiveSV2SubmitJob(msg.ChannelID, msg.JobID) {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, "stale-share")
		return responder.err
	}
	if err != nil {
		responder.writeSubmitSV2ErrorCode(msg.SequenceNumber, c.classifySV2SubmitMappingError(msg.ChannelID, msg.JobID, err))
		return responder.err
	}
	task, ok := c.mc.prepareStratumV2SubmissionTask(norm, responder, time.Now())
	if !ok {
		return responder.err
	}
	c.mc.processSubmissionTask(task)
	return responder.err
}

func readOneStratumV2FrameFromReader(r io.Reader) ([]byte, error) {
	var hdr [stratumV2FrameHeaderLen]byte
	n, err := io.ReadFull(r, hdr[:])
	if err != nil {
		if err == io.EOF && n == 0 {
			return nil, io.EOF
		}
		if err == io.ErrUnexpectedEOF && n == 0 {
			return nil, io.EOF
		}
		return nil, err
	}
	payloadLen := int(readUint24LE(hdr[3:6]))
	out := make([]byte, stratumV2FrameHeaderLen+payloadLen)
	copy(out[:stratumV2FrameHeaderLen], hdr[:])
	if payloadLen == 0 {
		return out, nil
	}
	if _, err := io.ReadFull(r, out[stratumV2FrameHeaderLen:]); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *sv2Conn) allocateChannelID() uint32 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.nextChannelID == 0 {
		c.nextChannelID = 1
	}
	id := c.nextChannelID
	c.nextChannelID++
	return id
}

func (c *sv2Conn) allocateWireJobID() uint32 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.nextWireJobID == 0 {
		c.nextWireJobID = 1
	}
	id := c.nextWireJobID
	c.nextWireJobID++
	return id
}

func (c *sv2Conn) currentSV2TargetBytes() [32]byte {
	if c == nil || c.mc == nil {
		return [32]byte{}
	}
	return uint256LEFromBigInt(c.mc.shareTargetOrDefault())
}

func reverseU256Bytes(in [32]byte) [32]byte {
	var out [32]byte
	for i := 0; i < 32; i++ {
		out[i] = in[31-i]
	}
	return out
}

func (c *sv2Conn) standardSV2MerkleRootU256LE(channelID uint32, job *Job) ([32]byte, error) {
	if c == nil || c.mc == nil || job == nil {
		return [32]byte{}, fmt.Errorf("missing sv2 conn miner or job")
	}
	c.stateMu.RLock()
	mapper := c.submitMapper
	var ch stratumV2SubmitChannelMapping
	exists := false
	if mapper != nil {
		ch, exists = mapper.channels[channelID]
		if len(ch.StandardExtranonce2) > 0 {
			ch.StandardExtranonce2 = append([]byte(nil), ch.StandardExtranonce2...)
		}
		if len(ch.ExtranoncePrefix) > 0 {
			ch.ExtranoncePrefix = append([]byte(nil), ch.ExtranoncePrefix...)
		}
	}
	c.stateMu.RUnlock()
	if mapper == nil {
		return [32]byte{}, fmt.Errorf("missing submit mapper")
	}
	if !exists {
		return [32]byte{}, fmt.Errorf("missing channel mapping for channel=%d", channelID)
	}
	worker := ch.WorkerName
	if worker == "" {
		worker = c.mc.currentWorker()
	}
	payoutScript := c.mc.singlePayoutScript(job, worker)
	if len(payoutScript) == 0 {
		// Fallback keeps SV2 job encoding available in test fixtures or early
		// sessions where worker-specific wallet resolution is not yet populated.
		payoutScript = job.PayoutScript
	}
	if len(payoutScript) == 0 {
		return [32]byte{}, fmt.Errorf("missing payout script for worker=%q", worker)
	}
	en2 := ch.StandardExtranonce2
	if len(en2) == 0 {
		en2 = make([]byte, c.mc.cfg.Extranonce2Size)
		if len(en2) == 0 {
			en2 = make([]byte, 4)
		}
	}
	scriptTime := c.mc.scriptTimeForJob(job.JobID, job.ScriptTime)
	_, cbTxid, err := serializeCoinbaseTxPredecoded(
		job.Template.Height,
		c.mc.stratumV1.extranonce1,
		en2,
		job.TemplateExtraNonce2Size,
		payoutScript,
		job.CoinbaseValue,
		job.witnessCommitScript,
		job.coinbaseFlagsBytes,
		job.CoinbaseMsg,
		scriptTime,
	)
	if err != nil || len(cbTxid) != 32 {
		if err == nil {
			err = fmt.Errorf("invalid coinbase txid len=%d", len(cbTxid))
		}
		return [32]byte{}, fmt.Errorf("compute sv2 standard merkle root coinbase: %w", err)
	}
	var merkleRootBE [32]byte
	var merkleOK bool
	if job.merkleBranchesBytes != nil {
		merkleRootBE, merkleOK = computeMerkleRootFromBranchesBytes32(cbTxid, job.merkleBranchesBytes)
	} else {
		merkleRootBE, merkleOK = computeMerkleRootFromBranches32(cbTxid, job.MerkleBranches)
	}
	if !merkleOK {
		return [32]byte{}, fmt.Errorf("compute sv2 standard merkle root failed")
	}
	return reverseU256Bytes(merkleRootBE), nil
}

func (c *sv2Conn) writeSetupConnectionSuccess(msg stratumV2WireSetupConnectionSuccess) error {
	frame, err := encodeStratumV2SetupConnectionSuccessFrame(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(frame)
}

func (c *sv2Conn) writeSetupConnectionError(msg stratumV2WireSetupConnectionError) error {
	frame, err := encodeStratumV2SetupConnectionErrorFrame(msg)
	if err != nil {
		return err
	}
	return c.writeFrame(frame)
}

func (c *sv2Conn) defaultLocalJobIDForWireNewMiningJob(msg stratumV2WireNewMiningJob) (string, bool) {
	if c == nil || c.mc == nil {
		return "", false
	}
	c.mc.jobMu.Lock()
	defer c.mc.jobMu.Unlock()
	if c.mc.lastJob != nil && c.mc.lastJob.JobID != "" {
		return c.mc.lastJob.JobID, true
	}
	if len(c.mc.activeJobs) == 1 {
		for _, job := range c.mc.activeJobs {
			if job != nil && job.JobID != "" {
				return job.JobID, true
			}
		}
	}
	return "", false
}

func (c *sv2Conn) applyStratumV2SetExtranoncePrefix(msg stratumV2WireSetExtranoncePrefix) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	ch, ok := c.submitMapper.channels[msg.ChannelID]
	if !ok {
		return
	}
	ch.ExtranoncePrefix = append([]byte(nil), msg.ExtranoncePrefix...)
	c.submitMapper.registerChannel(msg.ChannelID, ch)
}

func (c *sv2Conn) applyStratumV2NewMiningJob(msg stratumV2WireNewMiningJob, localJobID string) {
	if c == nil || localJobID == "" {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.submitMapper == nil {
		c.submitMapper = newStratumV2SubmitMapperState()
	}
	c.submitMapper.registerJob(msg.ChannelID, msg.JobID, localJobID)
}

func (c *sv2Conn) writeStratumV2SetExtranoncePrefix(msg stratumV2WireSetExtranoncePrefix) error {
	frame, err := encodeStratumV2SetExtranoncePrefixFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2SetExtranoncePrefix(msg)
	return nil
}

func (c *sv2Conn) writeStratumV2NewMiningJob(msg stratumV2WireNewMiningJob, localJobID string) error {
	frame, err := encodeStratumV2NewMiningJobFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2NewMiningJob(msg, localJobID)
	return nil
}

func (c *sv2Conn) applyStratumV2SetTarget(msg stratumV2WireSetTarget) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.channelTargets == nil {
		c.channelTargets = make(map[uint32][32]byte)
	}
	c.channelTargets[msg.ChannelID] = msg.MaximumTarget
}

func (c *sv2Conn) writeStratumV2SetTarget(msg stratumV2WireSetTarget) error {
	frame, err := encodeStratumV2SetTargetFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2SetTarget(msg)
	return nil
}

func (c *sv2Conn) applyStratumV2SetNewPrevHash(msg stratumV2WireSetNewPrevHash) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.channelPrevHash == nil {
		c.channelPrevHash = make(map[uint32]stratumV2WireSetNewPrevHash)
	}
	c.channelPrevHash[msg.ChannelID] = msg
}

func (c *sv2Conn) writeStratumV2SetNewPrevHash(msg stratumV2WireSetNewPrevHash) error {
	frame, err := encodeStratumV2SetNewPrevHashFrame(msg)
	if err != nil {
		return err
	}
	if err := c.writeFrame(frame); err != nil {
		return err
	}
	c.applyStratumV2SetNewPrevHash(msg)
	return nil
}

// writeStratumV2JobBundleForLocalJob emits the core mining updates needed for a
// channel to start hashing on a pool Job and keeps SV2 connection state/mappers
// in sync. Merkle root derivation for standard-channel NewMiningJob is left as
// a follow-up; this emits the consensus header fields and local/wire job mapping.
func (c *sv2Conn) writeStratumV2JobBundleForLocalJob(channelID uint32, wireJobID uint32, job *Job) error {
	if c == nil || job == nil {
		return fmt.Errorf("missing sv2 conn or job")
	}
	if c.mc != nil {
		// Keep the shared submit parser's per-connection job cache in sync with
		// SV2 job announcements, since the current SV2 bridge still reuses v1-era
		// job lookup/validation helpers.
		c.mc.trackJob(job, c.mc.cleanFlagFor(job))
		c.mc.setJobDifficulty(job.JobID, c.mc.currentDifficulty())
		if job.VersionMask != 0 {
			c.mc.stratumV1.versionRoll = true
			if c.mc.stratumV1.minerMask == 0 {
				c.mc.stratumV1.minerMask = job.VersionMask
			}
			c.mc.updateVersionMask(job.VersionMask)
		}
	}
	nbits, err := parseUint32BEHex(job.Template.Bits)
	if err != nil {
		return fmt.Errorf("parse job bits: %w", err)
	}
	prevHash := job.prevHashBytes
	if prevHash == ([32]byte{}) && job.Template.Previous != "" {
		if err := decodeHexToFixedBytes(prevHash[:], job.Template.Previous); err != nil {
			return fmt.Errorf("decode prevhash: %w", err)
		}
	}
	merkleRootLE, err := c.standardSV2MerkleRootU256LE(channelID, job)
	if err != nil {
		return err
	}
	if err := c.writeStratumV2SetTarget(stratumV2WireSetTarget{
		ChannelID:     channelID,
		MaximumTarget: c.currentSV2TargetBytes(),
	}); err != nil {
		return err
	}
	if err := c.writeStratumV2NewMiningJob(stratumV2WireNewMiningJob{
		ChannelID:  channelID,
		JobID:      wireJobID,
		Version:    uint32(job.Template.Version),
		MerkleRoot: merkleRootLE,
	}, job.JobID); err != nil {
		return err
	}
	return c.writeStratumV2SetNewPrevHash(stratumV2WireSetNewPrevHash{
		ChannelID: channelID,
		JobID:     wireJobID,
		PrevHash:  reverseU256Bytes(prevHash),
		MinNTime:  uint32(job.Template.CurTime),
		NBits:     nbits,
	})
}

func (c *sv2Conn) writeStratumV2JobBundleForAllChannels(job *Job) error {
	if c == nil || job == nil {
		return fmt.Errorf("missing sv2 conn or job")
	}
	channelIDs := c.snapshotSV2ChannelIDs()
	if len(channelIDs) == 0 {
		return nil
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	for _, channelID := range channelIDs {
		if err := c.writeStratumV2JobBundleForLocalJob(channelID, c.allocateWireJobID(), job); err != nil {
			return err
		}
	}
	return nil
}

func (c *sv2Conn) writeStratumV2SetTargetForAllChannels() error {
	if c == nil {
		return fmt.Errorf("missing sv2 conn")
	}
	channelIDs := c.snapshotSV2ChannelIDs()
	if len(channelIDs) == 0 {
		return nil
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	target := c.currentSV2TargetBytes()
	for _, channelID := range channelIDs {
		if err := c.writeStratumV2SetTarget(stratumV2WireSetTarget{
			ChannelID:     channelID,
			MaximumTarget: target,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *sv2Conn) writeCurrentJobBundleForChannel(channelID uint32) error {
	if c == nil || c.mc == nil {
		return nil
	}
	var job *Job
	if c.mc.jobMgr != nil {
		job = c.mc.jobMgr.CurrentJob()
	}
	if job == nil {
		c.mc.jobMu.Lock()
		if c.mc.lastJob != nil {
			job = c.mc.lastJob
		} else if len(c.mc.activeJobs) == 1 {
			for _, j := range c.mc.activeJobs {
				job = j
			}
		}
		c.mc.jobMu.Unlock()
	}
	if job == nil {
		return nil
	}
	return c.writeStratumV2JobBundleForLocalJob(channelID, c.allocateWireJobID(), job)
}

func (c *sv2Conn) classifySV2SubmitMappingError(channelID uint32, wireJobID uint32, err error) string {
	if err == nil {
		return "invalid-job-id"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unknown channel id") {
		return "invalid-channel-id"
	}
	if strings.Contains(msg, "unknown job mapping") {
		c.stateMu.RLock()
		active, ok := c.channelPrevHash[channelID]
		c.stateMu.RUnlock()
		if ok && active.JobID != 0 && active.JobID != wireJobID {
			return "stale-share"
		}
		return "invalid-job-id"
	}
	if strings.Contains(msg, "extranonce") {
		return "invalid-job-id"
	}
	return "invalid-job-id"
}

func (c *sv2Conn) isActiveSV2SubmitJob(channelID uint32, wireJobID uint32) bool {
	if c == nil {
		return true
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.channelPrevHash == nil {
		return true
	}
	active, ok := c.channelPrevHash[channelID]
	if !ok || active.JobID == 0 {
		return true
	}
	return active.JobID == wireJobID
}

func (c *sv2Conn) snapshotSV2ChannelIDs() []uint32 {
	if c == nil {
		return nil
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.submitMapper == nil || len(c.submitMapper.channels) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(c.submitMapper.channels))
	for id := range c.submitMapper.channels {
		out = append(out, id)
	}
	return out
}

func writeStratumV2SetTargetToWriter(w io.Writer, msg stratumV2WireSetTarget) error {
	frame, err := encodeStratumV2SetTargetFrame(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

type stratumV2SubmitWireResponder struct {
	mc             *MinerConn
	w              io.Writer
	writeFrame     func([]byte) error
	channelID      uint32
	sequenceNumber uint32
	err            error
}

func (r *stratumV2SubmitWireResponder) writeEncodedFrame(frame []byte) error {
	if r == nil {
		return io.ErrClosedPipe
	}
	if r.writeFrame != nil {
		return r.writeFrame(frame)
	}
	if r.w == nil {
		return io.ErrClosedPipe
	}
	_, err := r.w.Write(frame)
	return err
}

func (r *stratumV2SubmitWireResponder) writeSubmitOK(reqID any) {
	if r == nil || r.err != nil {
		return
	}
	seq := r.sequenceNumber
	if v, ok := reqID.(uint32); ok && v != 0 {
		seq = v
	}
	frame, err := encodeStratumV2SubmitSharesSuccessFrame(stratumV2WireSubmitSharesSuccess{
		ChannelID:               r.channelID,
		LastSequenceNumber:      seq,
		NewSubmitsAcceptedCount: 1,
		// Until the shared submit core plumbs exact share-difficulty credits into
		// the SV2 responder, report at least one accepted share unit so miners
		// don't display a stuck zero accepted-share counter.
		NewSharesSum: 1,
	})
	if err != nil {
		r.err = err
		return
	}
	r.err = r.writeEncodedFrame(frame)
}

func (r *stratumV2SubmitWireResponder) writeSubmitError(reqID any, errCode int, msg string, banned bool) {
	if r == nil || r.err != nil {
		return
	}
	seq := r.sequenceNumber
	if v, ok := reqID.(uint32); ok && v != 0 {
		seq = v
	}
	frame, err := encodeStratumV2SubmitSharesErrorFrame(stratumV2WireSubmitSharesError{
		ChannelID:      r.channelID,
		SequenceNumber: seq,
		ErrorCode:      mapStratumErrorToSv2SubmitErrorCode(errCode, msg, banned),
	})
	if err != nil {
		r.err = err
		return
	}
	r.err = r.writeEncodedFrame(frame)
}

func (r *stratumV2SubmitWireResponder) writeSubmitSV2ErrorCode(reqID any, code string) {
	if r == nil || r.err != nil {
		return
	}
	seq := r.sequenceNumber
	if v, ok := reqID.(uint32); ok && v != 0 {
		seq = v
	}
	frame, err := encodeStratumV2SubmitSharesErrorFrame(stratumV2WireSubmitSharesError{
		ChannelID:      r.channelID,
		SequenceNumber: seq,
		ErrorCode:      code,
	})
	if err != nil {
		r.err = err
		return
	}
	r.err = r.writeEncodedFrame(frame)
}

func (r *stratumV2SubmitWireResponder) sendSetTarget(job *Job) {
	if r == nil || r.err != nil {
		return
	}
	var target [32]byte
	if r.mc != nil {
		target = uint256LEFromBigInt(r.mc.shareTargetOrDefault())
	}
	frame, err := encodeStratumV2SetTargetFrame(stratumV2WireSetTarget{
		ChannelID:     r.channelID,
		MaximumTarget: target,
	})
	if err != nil {
		r.err = err
		return
	}
	if err := r.writeEncodedFrame(frame); err != nil {
		r.err = err
		return
	}
}

func mapStratumErrorToSv2SubmitErrorCode(errCode int, msg string, banned bool) string {
	msgLower := strings.ToLower(strings.TrimSpace(msg))
	if banned {
		return "unauthorized"
	}
	switch errCode {
	case stratumErrCodeUnauthorized:
		return "unauthorized"
	case stratumErrCodeLowDiffShare:
		return "difficulty-too-low"
	case stratumErrCodeJobNotFound:
		if strings.Contains(msgLower, "stale") {
			return "stale-share"
		}
		return "invalid-job-id"
	case stratumErrCodeDuplicateShare:
		return "duplicate-share"
	case stratumErrCodeInvalidRequest:
		switch {
		case strings.Contains(msgLower, "job"):
			return "invalid-job-id"
		case strings.Contains(msgLower, "ntime"), strings.Contains(msgLower, "time"):
			return "invalid-timestamp"
		case strings.Contains(msgLower, "extranonce"):
			return "invalid-extranonce"
		case strings.Contains(msgLower, "version"):
			return "invalid-version"
		case strings.Contains(msgLower, "nonce"),
			strings.Contains(msgLower, "coinbase"),
			strings.Contains(msgLower, "merkle"):
			return "invalid-solution"
		case strings.Contains(msgLower, "unauthorized"), strings.Contains(msgLower, "banned"):
			return "unauthorized"
		}
		return "invalid-job-id"
	default:
		if strings.Contains(msgLower, "duplicate") {
			return "duplicate-share"
		}
		if strings.Contains(msgLower, "low diff") || strings.Contains(msgLower, "difficulty") {
			return "difficulty-too-low"
		}
		if strings.Contains(msgLower, "stale") {
			return "stale-share"
		}
		if strings.Contains(msgLower, "ntime") || strings.Contains(msgLower, "time") {
			return "invalid-timestamp"
		}
		if strings.Contains(msgLower, "extranonce") {
			return "invalid-extranonce"
		}
		if strings.Contains(msgLower, "version") {
			return "invalid-version"
		}
		return "invalid-job-id"
	}
}

// helper used by tests/debugging for peeking at encoded success sequence.
func decodeSv2SubmitSuccessLastSeq(frame []byte) (uint32, error) {
	msg, err := decodeStratumV2SubmitWireFrame(frame)
	if err != nil {
		return 0, err
	}
	s, ok := msg.(stratumV2WireSubmitSharesSuccess)
	if !ok {
		return 0, fmt.Errorf("not submitshares.success: %T", msg)
	}
	return s.LastSequenceNumber, nil
}

// helper used by tests/debugging for peeking at encoded error sequence.
func decodeSv2SubmitErrorSeq(frame []byte) (uint32, error) {
	msg, err := decodeStratumV2SubmitWireFrame(frame)
	if err != nil {
		return 0, err
	}
	e, ok := msg.(stratumV2WireSubmitSharesError)
	if !ok {
		return 0, fmt.Errorf("not submitshares.error: %T", msg)
	}
	return e.SequenceNumber, nil
}
