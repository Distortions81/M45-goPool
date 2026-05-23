package main

const (
	sv2MsgSetupConnection              byte = 0x00
	sv2MsgSetupConnectionSuccess       byte = 0x01
	sv2MsgSetupConnectionError         byte = 0x02
	sv2MsgOpenStandardMiningChannel    byte = 0x10
	sv2MsgOpenStdMiningChannelSuccess  byte = 0x11
	sv2MsgOpenMiningChannelError       byte = 0x12
	sv2MsgOpenStdMiningChannelError    byte = sv2MsgOpenMiningChannelError
	sv2MsgOpenExtendedMiningChannel    byte = 0x13
	sv2MsgOpenExtMiningChannelSuccess  byte = 0x14
	sv2MsgOpenExtMiningChannelError    byte = sv2MsgOpenMiningChannelError
	sv2MsgNewMiningJob                 byte = 0x15
	sv2MsgUpdateChannel                byte = 0x16
	sv2MsgCloseChannel                 byte = 0x18
	sv2MsgSetNewPrevHash               byte = 0x20
	sv2MsgSetTarget                    byte = 0x21
	sv2MsgNewExtendedMiningJob         byte = 0x1f
	sv2MsgSubmitSharesStandard         byte = 0x1a
	sv2MsgSubmitSharesExtended         byte = 0x1b
	sv2MsgSubmitSharesSuccess          byte = 0x1c
	sv2MsgSubmitSharesError            byte = 0x1d
)

// sv2SetupConnection (0x00): miner → pool
type sv2SetupConnection struct {
	Protocol        uint8
	MinVersion      uint16
	MaxVersion      uint16
	Flags           uint32
	EndpointHost    string
	EndpointPort    uint16
	Vendor          string
	HardwareVersion string
	Firmware        string
	DeviceID        string
}

func (m *sv2SetupConnection) decode(b []byte) error {
	off := 0
	var err error
	if m.Protocol, err = sv2ReadU8(b, &off); err != nil {
		return err
	}
	if m.MinVersion, err = sv2ReadU16(b, &off); err != nil {
		return err
	}
	if m.MaxVersion, err = sv2ReadU16(b, &off); err != nil {
		return err
	}
	if m.Flags, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.EndpointHost, err = sv2ReadStr(b, &off); err != nil {
		return err
	}
	if m.EndpointPort, err = sv2ReadU16(b, &off); err != nil {
		return err
	}
	if m.Vendor, err = sv2ReadStr(b, &off); err != nil {
		return err
	}
	if m.HardwareVersion, err = sv2ReadStr(b, &off); err != nil {
		return err
	}
	if m.Firmware, err = sv2ReadStr(b, &off); err != nil {
		return err
	}
	m.DeviceID, err = sv2ReadStr(b, &off)
	return err
}

func (m *sv2SetupConnection) encode() []byte {
	var b []byte
	b = sv2AppendU8(b, m.Protocol)
	b = sv2AppendU16(b, m.MinVersion)
	b = sv2AppendU16(b, m.MaxVersion)
	b = sv2AppendU32(b, m.Flags)
	b = sv2AppendStr(b, m.EndpointHost)
	b = sv2AppendU16(b, m.EndpointPort)
	b = sv2AppendStr(b, m.Vendor)
	b = sv2AppendStr(b, m.HardwareVersion)
	b = sv2AppendStr(b, m.Firmware)
	b = sv2AppendStr(b, m.DeviceID)
	return b
}

// sv2SetupConnectionSuccess (0x01): pool → miner
type sv2SetupConnectionSuccess struct {
	UsedVersion uint16
	Flags       uint32
}

func (m *sv2SetupConnectionSuccess) encode() []byte {
	var b []byte
	b = sv2AppendU16(b, m.UsedVersion)
	b = sv2AppendU32(b, m.Flags)
	return b
}

func (m *sv2SetupConnectionSuccess) decode(b []byte) error {
	off := 0
	var err error
	if m.UsedVersion, err = sv2ReadU16(b, &off); err != nil {
		return err
	}
	m.Flags, err = sv2ReadU32(b, &off)
	return err
}

// sv2SetupConnectionError (0x02): pool → miner
type sv2SetupConnectionError struct {
	Flags     uint32
	ErrorCode string
}

func (m *sv2SetupConnectionError) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.Flags)
	b = sv2AppendStr(b, m.ErrorCode)
	return b
}

func (m *sv2SetupConnectionError) decode(b []byte) error {
	off := 0
	var err error
	if m.Flags, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.ErrorCode, err = sv2ReadStr(b, &off)
	return err
}

// sv2OpenStandardMiningChannel (0x10): miner → pool
type sv2OpenStandardMiningChannel struct {
	RequestID       uint32
	UserIdentity    string
	NominalHashRate float32
	MaxTarget       [32]byte
}

func (m *sv2OpenStandardMiningChannel) decode(b []byte) error {
	off := 0
	var err error
	if m.RequestID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.UserIdentity, err = sv2ReadStr(b, &off); err != nil {
		return err
	}
	if m.NominalHashRate, err = sv2ReadF32(b, &off); err != nil {
		return err
	}
	m.MaxTarget, err = sv2ReadU256(b, &off)
	return err
}

func (m *sv2OpenStandardMiningChannel) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.RequestID)
	b = sv2AppendStr(b, m.UserIdentity)
	b = sv2AppendF32(b, m.NominalHashRate)
	b = sv2AppendU256(b, m.MaxTarget)
	return b
}

// sv2OpenStdMiningChannelSuccess (0x11): pool → miner
type sv2OpenStdMiningChannelSuccess struct {
	RequestID        uint32
	ChannelID        uint32
	Target           [32]byte
	ExtraNoncePrefix []byte // B0_32: u8 length prefix, 0-32 bytes
	GroupChannelID   uint32
}

func (m *sv2OpenStdMiningChannelSuccess) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.RequestID)
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU256(b, m.Target)
	b = sv2AppendBytes(b, m.ExtraNoncePrefix) // u8 length prefix
	b = sv2AppendU32(b, m.GroupChannelID)
	return b
}

func (m *sv2OpenStdMiningChannelSuccess) decode(b []byte) error {
	off := 0
	var err error
	if m.RequestID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.Target, err = sv2ReadU256(b, &off); err != nil {
		return err
	}
	if m.ExtraNoncePrefix, err = sv2ReadBytes(b, &off); err != nil {
		return err
	}
	m.GroupChannelID, err = sv2ReadU32(b, &off)
	return err
}

// sv2OpenStdMiningChannelError (0x12): pool → miner
type sv2OpenStdMiningChannelError struct {
	RequestID uint32
	ErrorCode string
}

func (m *sv2OpenStdMiningChannelError) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.RequestID)
	b = sv2AppendStr(b, m.ErrorCode)
	return b
}

func (m *sv2OpenStdMiningChannelError) decode(b []byte) error {
	off := 0
	var err error
	if m.RequestID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.ErrorCode, err = sv2ReadStr(b, &off)
	return err
}

// sv2OpenExtendedMiningChannel (0x13): miner → pool
type sv2OpenExtendedMiningChannel struct {
	RequestID         uint32
	UserIdentity      string
	NominalHashRate   float32
	MaxTarget         [32]byte
	MinExtranonceSize uint16
}

func (m *sv2OpenExtendedMiningChannel) decode(b []byte) error {
	off := 0
	var err error
	if m.RequestID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.UserIdentity, err = sv2ReadStr(b, &off); err != nil {
		return err
	}
	if m.NominalHashRate, err = sv2ReadF32(b, &off); err != nil {
		return err
	}
	if m.MaxTarget, err = sv2ReadU256(b, &off); err != nil {
		return err
	}
	m.MinExtranonceSize, err = sv2ReadU16(b, &off)
	return err
}

func (m *sv2OpenExtendedMiningChannel) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.RequestID)
	b = sv2AppendStr(b, m.UserIdentity)
	b = sv2AppendF32(b, m.NominalHashRate)
	b = sv2AppendU256(b, m.MaxTarget)
	b = sv2AppendU16(b, m.MinExtranonceSize)
	return b
}

// sv2OpenExtMiningChannelSuccess (0x14): pool → miner
type sv2OpenExtMiningChannelSuccess struct {
	RequestID        uint32
	ChannelID        uint32
	Target           [32]byte
	ExtranonceSize   uint16
	ExtraNoncePrefix []byte // B0_32: u8 length prefix, 0-32 bytes
	GroupChannelID   uint32
}

func (m *sv2OpenExtMiningChannelSuccess) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.RequestID)
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU256(b, m.Target)
	b = sv2AppendU16(b, m.ExtranonceSize)
	b = sv2AppendBytes(b, m.ExtraNoncePrefix) // u8 length prefix
	b = sv2AppendU32(b, m.GroupChannelID)
	return b
}

func (m *sv2OpenExtMiningChannelSuccess) decode(b []byte) error {
	off := 0
	var err error
	if m.RequestID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.Target, err = sv2ReadU256(b, &off); err != nil {
		return err
	}
	if m.ExtranonceSize, err = sv2ReadU16(b, &off); err != nil {
		return err
	}
	if m.ExtraNoncePrefix, err = sv2ReadBytes(b, &off); err != nil {
		return err
	}
	m.GroupChannelID, err = sv2ReadU32(b, &off)
	return err
}

// sv2OpenExtMiningChannelError (0x15): pool → miner
type sv2OpenExtMiningChannelError struct {
	RequestID uint32
	ErrorCode string
}

func (m *sv2OpenExtMiningChannelError) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.RequestID)
	b = sv2AppendStr(b, m.ErrorCode)
	return b
}

func (m *sv2OpenExtMiningChannelError) decode(b []byte) error {
	off := 0
	var err error
	if m.RequestID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.ErrorCode, err = sv2ReadStr(b, &off)
	return err
}

// sv2NewExtendedMiningJob (0x1f): pool → miner
type sv2NewExtendedMiningJob struct {
	ChannelID             uint32
	JobID                 uint32
	MinNTime              *uint32 // Sv2Option<U32>
	Version               uint32
	VersionRollingAllowed bool
	MerklePath            [][32]byte
	CbPrefix              []byte // B0_64K: u16 length prefix
	CbSuffix              []byte // B0_64K: u16 length prefix
}

func (m *sv2NewExtendedMiningJob) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.JobID)
	b = sv2AppendOptU32(b, m.MinNTime)
	b = sv2AppendU32(b, m.Version)
	b = sv2AppendBool(b, m.VersionRollingAllowed)
	b = sv2AppendU256Seq(b, m.MerklePath)
	b = sv2AppendBytesB0_64K(b, m.CbPrefix)
	b = sv2AppendBytesB0_64K(b, m.CbSuffix)
	return b
}

func (m *sv2NewExtendedMiningJob) decode(b []byte) error {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.JobID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.MinNTime, err = sv2ReadOptU32(b, &off); err != nil {
		return err
	}
	if m.Version, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.VersionRollingAllowed, err = sv2ReadBool(b, &off); err != nil {
		return err
	}
	if m.MerklePath, err = sv2ReadU256Seq(b, &off); err != nil {
		return err
	}
	if m.CbPrefix, err = sv2ReadBytesB0_64K(b, &off); err != nil {
		return err
	}
	m.CbSuffix, err = sv2ReadBytesB0_64K(b, &off)
	return err
}

// sv2NewMiningJob (0x15): pool -> miner
// Standard channels only: fixed merkle root, header-only mining.
type sv2NewMiningJob struct {
	ChannelID uint32
	JobID     uint32
	MinNTime  *uint32 // Sv2Option<U32>
	Version   uint32
	MerkleRoot [32]byte
}

func (m *sv2NewMiningJob) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.JobID)
	b = sv2AppendOptU32(b, m.MinNTime)
	b = sv2AppendU32(b, m.Version)
	b = sv2AppendU256(b, m.MerkleRoot)
	return b
}

func (m *sv2NewMiningJob) decode(b []byte) error {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.JobID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.MinNTime, err = sv2ReadOptU32(b, &off); err != nil {
		return err
	}
	if m.Version, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.MerkleRoot, err = sv2ReadU256(b, &off)
	return err
}

// sv2SetNewPrevHash (0x20): pool → miner
type sv2SetNewPrevHash struct {
	ChannelID uint32
	JobID     uint32
	PrevHash  [32]byte
	MinNTime  uint32
	NBits     uint32
}

func (m *sv2SetNewPrevHash) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.JobID)
	b = sv2AppendU256(b, m.PrevHash)
	b = sv2AppendU32(b, m.MinNTime)
	b = sv2AppendU32(b, m.NBits)
	return b
}

func (m *sv2SetNewPrevHash) decode(b []byte) error {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.JobID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.PrevHash, err = sv2ReadU256(b, &off); err != nil {
		return err
	}
	if m.MinNTime, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.NBits, err = sv2ReadU32(b, &off)
	return err
}

// sv2SetTarget (0x21): pool → miner
type sv2SetTarget struct {
	ChannelID     uint32
	MaximumTarget [32]byte
}

func (m *sv2SetTarget) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU256(b, m.MaximumTarget)
	return b
}

func (m *sv2SetTarget) decode(b []byte) error {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.MaximumTarget, err = sv2ReadU256(b, &off)
	return err
}

// sv2SubmitSharesStandard (0x1a): miner → pool
type sv2SubmitSharesStandard struct {
	ChannelID      uint32
	SequenceNumber uint32
	JobID          uint32
	Nonce          uint32
	NTime          uint32
	Version        uint32
}

// sv2SubmitSharesExtended (0x1b): miner → pool
type sv2SubmitSharesExtended struct {
	ChannelID      uint32
	SequenceNumber uint32
	JobID          uint32
	Nonce          uint32
	NTime          uint32
	Version        uint32
	Extranonce     []byte // B0_32: u8 length prefix, 0-32 bytes
}

func (m *sv2SubmitSharesExtended) decode(b []byte) error {
	_, err := m.decodeWithTrailing(b)
	return err
}

func (m *sv2SubmitSharesExtended) decodeWithTrailing(b []byte) ([]byte, error) {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.SequenceNumber, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.JobID, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.Nonce, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.NTime, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.Version, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	m.Extranonce, err = sv2ReadBytes(b, &off)
	if err != nil {
		return nil, err
	}
	if off >= len(b) {
		return nil, nil
	}
	trailing := make([]byte, len(b)-off)
	copy(trailing, b[off:])
	return trailing, nil
}

func (m *sv2SubmitSharesExtended) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.SequenceNumber)
	b = sv2AppendU32(b, m.JobID)
	b = sv2AppendU32(b, m.Nonce)
	b = sv2AppendU32(b, m.NTime)
	b = sv2AppendU32(b, m.Version)
	b = sv2AppendBytes(b, m.Extranonce)
	return b
}

func (m *sv2SubmitSharesStandard) decode(b []byte) error {
	_, err := m.decodeWithTrailing(b)
	return err
}

func (m *sv2SubmitSharesStandard) decodeWithTrailing(b []byte) ([]byte, error) {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.SequenceNumber, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.JobID, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.Nonce, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	if m.NTime, err = sv2ReadU32(b, &off); err != nil {
		return nil, err
	}
	m.Version, err = sv2ReadU32(b, &off)
	if err != nil {
		return nil, err
	}
	if off >= len(b) {
		return nil, nil
	}
	trailing := make([]byte, len(b)-off)
	copy(trailing, b[off:])
	return trailing, nil
}

func (m *sv2SubmitSharesStandard) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.SequenceNumber)
	b = sv2AppendU32(b, m.JobID)
	b = sv2AppendU32(b, m.Nonce)
	b = sv2AppendU32(b, m.NTime)
	b = sv2AppendU32(b, m.Version)
	return b
}

// sv2SubmitSharesSuccess (0x1c): pool → miner
type sv2SubmitSharesSuccess struct {
	ChannelID               uint32
	LastSequenceNumber      uint32
	NewSubmitsAcceptedCount uint32
	NewSharesSum            uint64
}

func (m *sv2SubmitSharesSuccess) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.LastSequenceNumber)
	b = sv2AppendU32(b, m.NewSubmitsAcceptedCount)
	b = sv2AppendU64(b, m.NewSharesSum)
	return b
}

func (m *sv2SubmitSharesSuccess) decode(b []byte) error {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.LastSequenceNumber, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.NewSubmitsAcceptedCount, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.NewSharesSum, err = sv2ReadU64(b, &off)
	return err
}

// sv2SubmitSharesError (0x1d): pool → miner
type sv2SubmitSharesError struct {
	ChannelID      uint32
	SequenceNumber uint32
	ErrorCode      string
}

func (m *sv2SubmitSharesError) encode() []byte {
	var b []byte
	b = sv2AppendU32(b, m.ChannelID)
	b = sv2AppendU32(b, m.SequenceNumber)
	b = sv2AppendStr(b, m.ErrorCode)
	return b
}

func (m *sv2SubmitSharesError) decode(b []byte) error {
	off := 0
	var err error
	if m.ChannelID, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	if m.SequenceNumber, err = sv2ReadU32(b, &off); err != nil {
		return err
	}
	m.ErrorCode, err = sv2ReadStr(b, &off)
	return err
}
