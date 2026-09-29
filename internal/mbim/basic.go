package mbim

import "context"

// DeviceCaps は Basic Connect DEVICE_CAPS の応答（probe / status の疎通確認と表示用）。
type DeviceCaps struct {
	DeviceType    uint32
	CellularClass uint32
	SIMClass      uint32
	DataClass     uint32
	MaxSessions   uint32
	DeviceID      string // IMEI など
	FirmwareInfo  string
	HardwareInfo  string
}

// ParseDeviceCaps は DEVICE_CAPS の情報バッファを解析する。
func ParseDeviceCaps(buf []byte) (DeviceCaps, error) {
	d := NewDecoder(buf)
	var c DeviceCaps
	c.DeviceType = d.U32()
	c.CellularClass = d.U32()
	_ = d.U32() // VoiceClass
	c.SIMClass = d.U32()
	c.DataClass = d.U32()
	_ = d.U32() // SmsCaps
	_ = d.U32() // ControlCaps
	c.MaxSessions = d.U32()
	_ = d.String() // CustomDataClass
	c.DeviceID = d.String()
	c.FirmwareInfo = d.String()
	c.HardwareInfo = d.String()
	return c, d.Err()
}

// EncodeDeviceCaps は DEVICE_CAPS の情報バッファを組み立てる（fake proxy 用）。
func EncodeDeviceCaps(c DeviceCaps) []byte {
	return Encode(
		U32(c.DeviceType), U32(c.CellularClass), U32(0), U32(c.SIMClass), U32(c.DataClass),
		U32(0), U32(0), U32(c.MaxSessions),
		String(""), String(c.DeviceID), String(c.FirmwareInfo), String(c.HardwareInfo),
	)
}

// DeviceCaps は DEVICE_CAPS を Query する。
func (c *Client) DeviceCaps(ctx context.Context) (DeviceCaps, error) {
	buf, err := c.Query(ctx, ServiceBasicConnect, CIDBasicConnectDeviceCaps, nil)
	if err != nil {
		return DeviceCaps{}, err
	}
	return ParseDeviceCaps(buf)
}
