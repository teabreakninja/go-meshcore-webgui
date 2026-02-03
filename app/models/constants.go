package models

// Define constants for CommandCodes
const (
	CMD_AppStart          = 0x01 // 1
	CMD_SendTxtMsg        = 0x02 // 2
	CMD_SendChannelTxtMsg = 0x03 // 3
	CMD_GetContacts       = 0x04 // 4
	CMD_GetDeviceTime     = 0x05 // 5
	CMD_SetDeviceTime     = 0x06 // 6
	CMD_SendSelfAdvert    = 0x07 // 7
	CMD_SetAdvertName     = 0x08 // 8
	CMD_AddUpdateContact  = 0x09 // 9
	CMD_SyncNextMessage   = 0x0A // 10
	CMD_SetRadioParams    = 0x0B // 11
	CMD_SetTxPower        = 0x0C // 12
	CMD_ResetPath         = 0x0D // 13
	CMD_SetAdvertLatLon   = 0x0E // 14
	CMD_RemoveContact     = 0x0F // 15
	CMD_ShareContact      = 0x10 // 16
	CMD_ExportContact     = 0x11 // 17
	CMD_ImportContact     = 0x12 // 18
	CMD_Reboot            = 0x13 // 19
	CMD_GetBatteryVoltage = 0x14 // 20
	CMD_SetTuningParams   = 0x15 // 21
	CMD_DeviceQuery       = 0x16 // 22
	CMD_ExportPrivateKey  = 0x17 // 23
	CMD_ImportPrivateKey  = 0x18 // 24
	CMD_SendRawData       = 0x19 // 25
	CMD_SendLogin         = 0x1A // 26
	CMD_SendStatusReq     = 0x1B // 27
	CMD_GetChannel        = 0x1F // 31
	CMD_SetChannel        = 0x20 // 32
	CMD_SignStart         = 0x21 // 33
	CMD_SignData          = 0x22 // 34
	CMD_SignFinish        = 0x23 // 35
	CMD_SendTracePath     = 0x24 // 36
	CMD_SetOtherParams    = 0x26 // 38
	CMD_SendTelemetryReq  = 0x27 // 39
	CMD_SendBinaryReq     = 0x32 // 50
)

// Map of command names to their values
var CommandCodes = map[string]byte{
	"AppStart":          CMD_AppStart,
	"SendTxtMsg":        CMD_SendTxtMsg,
	"SendChannelTxtMsg": CMD_SendChannelTxtMsg,
	"GetContacts":       CMD_GetContacts,
	"GetDeviceTime":     CMD_GetDeviceTime,
	"SetDeviceTime":     CMD_SetDeviceTime,
	"SendSelfAdvert":    CMD_SendSelfAdvert,
	"SetAdvertName":     CMD_SetAdvertName,
	"AddUpdateContact":  CMD_AddUpdateContact,
	"SyncNextMessage":   CMD_SyncNextMessage,
	"SetRadioParams":    CMD_SetRadioParams,
	"SetTxPower":        CMD_SetTxPower,
	"ResetPath":         CMD_ResetPath,
	"SetAdvertLatLon":   CMD_SetAdvertLatLon,
	"RemoveContact":     CMD_RemoveContact,
	"ShareContact":      CMD_ShareContact,
	"ExportContact":     CMD_ExportContact,
	"ImportContact":     CMD_ImportContact,
	"Reboot":            CMD_Reboot,
	"GetBatteryVoltage": CMD_GetBatteryVoltage,
	"SetTuningParams":   CMD_SetTuningParams,
	"DeviceQuery":       CMD_DeviceQuery,
	"ExportPrivateKey":  CMD_ExportPrivateKey,
	"ImportPrivateKey":  CMD_ImportPrivateKey,
	"SendRawData":       CMD_SendRawData,
	"SendLogin":         CMD_SendLogin,
	"SendStatusReq":     CMD_SendStatusReq,
	"GetChannel":        CMD_GetChannel,
	"SetChannel":        CMD_SetChannel,
	"SignStart":         CMD_SignStart,
	"SignData":          CMD_SignData,
	"SignFinish":        CMD_SignFinish,
	"SendTracePath":     CMD_SendTracePath,
	"SetOtherParams":    CMD_SetOtherParams,
	"SendTelemetryReq":  CMD_SendTelemetryReq,
	"SendBinaryReq":     CMD_SendBinaryReq,
}

// Define a custom type for response codes
type ResponseCode byte

// Define the struct to hold the response codes
type ResponseCodesType struct {
	Ok             ResponseCode
	Err            ResponseCode
	ContactsStart  ResponseCode
	Contact        ResponseCode
	EndOfContacts  ResponseCode
	SelfInfo       ResponseCode
	Sent           ResponseCode
	ContactMsgRecv ResponseCode
	ChannelMsgRecv ResponseCode
	CurrTime       ResponseCode
	NoMoreMessages ResponseCode
	ExportContact  ResponseCode
	BatteryVoltage ResponseCode
	DeviceInfo     ResponseCode
	PrivateKey     ResponseCode
	Disabled       ResponseCode
	ChannelInfo    ResponseCode
	SignStart      ResponseCode
	Signature      ResponseCode
}

// Constructor to initialize the struct with the constants
func NewResponseCodes() *ResponseCodesType {
	return &ResponseCodesType{
		Ok:             0x00,
		Err:            0x01,
		ContactsStart:  0x02,
		Contact:        0x03,
		EndOfContacts:  0x04,
		SelfInfo:       0x05,
		Sent:           0x06,
		ContactMsgRecv: 0x07,
		ChannelMsgRecv: 0x08,
		CurrTime:       0x09,
		NoMoreMessages: 0x0A,
		ExportContact:  0x0B,
		BatteryVoltage: 0x0C,
		DeviceInfo:     0x0D,
		PrivateKey:     0x0E,
		Disabled:       0x0F,
		ChannelInfo:    0x12,
		SignStart:      0x13,
		Signature:      0x14,
	}
}

var ResponseCodes = NewResponseCodes()

// Push notification codes (unsolicited frames), from meshcore.js Constants.PushCodes
const (
	PushAdvert         = 0x80
	PushPathUpdated    = 0x81
	PushSendConfirmed  = 0x82
	PushMsgWaiting     = 0x83
	PushRawData        = 0x84
	PushLoginSuccess   = 0x85
	PushStatusResponse = 0x87
	PushLogRxData      = 0x88
	PushTraceData      = 0x89
	PushNewAdvert      = 0x8A
	PushTelemetry      = 0x8B
	PushBinaryResponse = 0x8C
)
