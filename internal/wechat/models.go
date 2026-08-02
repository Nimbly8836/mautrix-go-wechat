package wechat

import (
	"encoding/json"
	"strconv"
)

// API request/response structs, mirroring the subset of swagger.json the
// bridge uses. Field names match the WeChat API JSON exactly.

// --- Login ---

type proxyInfo struct {
	ProxyIp       string `json:"ProxyIp,omitempty"`
	ProxyUser     string `json:"ProxyUser,omitempty"`
	ProxyPassword string `json:"ProxyPassword,omitempty"`
}

// getQRReq -> Login.GetQRReq
type getQRReq struct {
	DeviceID   string     `json:"DeviceID,omitempty"`
	DeviceName string     `json:"DeviceName,omitempty"`
	LoginType  string     `json:"LoginType,omitempty"`
	Proxy      *proxyInfo `json:"Proxy,omitempty"`
}

// getQRResp is the Data of /Login/LoginGetQR. Field names are tolerant; the
// service returns at least a UUID and a base64/URL QR payload.
type getQRResp struct {
	Uuid        string        `json:"Uuid"`
	QRCode      string        `json:"QrCode"` // sometimes "QRCodeURL" / base64
	QRBase      string        `json:"QrBase64"`
	BaseUrl     string        `json:"BaseUrl"`
	ExpiredTime flexibleInt64 `json:"ExpiredTime"`
}

// flexibleInt64 accepts API numbers that may be encoded either as JSON numbers
// or as strings (e.g. LoginGetQRx returns ExpiredTime as "120").
type flexibleInt64 int64

func (n *flexibleInt64) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*n = flexibleInt64(int64Value(raw))
	return nil
}

// checkQRResp is the Data of /Login/LoginCheckQR.
type checkQRResp struct {
	Status      int           `json:"Status"` // 0=waiting, 1=scanned, 2=confirmed/success
	Wxid        string        `json:"Wxid"`
	NickName    string        `json:"NickName"`
	HeadImgUrl  string        `json:"HeadImgUrl"`
	ExpiredTime flexibleInt64 `json:"ExpiredTime"`
}

// --- Messaging ---

// sendTxtReq -> Msg.SendNewMsgParam
type sendTxtReq struct {
	Wxid    string `json:"Wxid"`
	ToWxid  string `json:"ToWxid"`
	Content string `json:"Content"`
	Type    int    `json:"Type"`
	At      string `json:"At,omitempty"`
}

// sendResp is the typical Data of a send call: contains the new message id.
type sendResp struct {
	NewMsgId    int64  `json:"NewMsgId"`
	MsgId       int64  `json:"MsgId"`
	ClientMsgId string `json:"ClientMsgId"`
	CreateTime  int64  `json:"CreateTime"`
}

// uploadImgReq -> /Msg/UploadImg
type uploadImgReq struct {
	Wxid   string `json:"Wxid"`
	ToWxid string `json:"ToWxid"`
	Base64 string `json:"Base64"` // base64 image bytes
}

type sendAppReq struct {
	Wxid   string `json:"Wxid"`
	ToWxid string `json:"ToWxid"`
	Type   int    `json:"Type"`
	Xml    string `json:"Xml"`
}

// sendVideoReq -> /Msg/SendVideo
// Base64 is the video bytes. ImageBase64 is an optional thumbnail.
type sendVideoReq struct {
	Wxid        string `json:"Wxid"`
	ToWxid      string `json:"ToWxid"`
	Base64      string `json:"Base64"`
	ImageBase64 string `json:"ImageBase64,omitempty"`
	PlayLength  int    `json:"PlayLength,omitempty"`
}

type uploadFileResp struct {
	MediaID  string
	TotalLen int64
}

func (r *uploadFileResp) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.MediaID = firstString(raw, "mediaId", "MediaId", "media_id", "MediaID", "AttachId", "attachid")
	r.TotalLen = int64Value(firstValue(raw, "totalLen", "TotalLen", "total_len", "DataLen", "length"))
	return nil
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func int64Value(v any) int64 {
	switch val := v.(type) {
	case int:
		return int64(val)
	case int64:
		return val
	case float64:
		return int64(val)
	case json.Number:
		i, _ := val.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(val, 10, 64)
		return i
	default:
		return 0
	}
}

type downloadSectionReq struct {
	StartPos int64 `json:"StartPos"`
	DataLen  int64 `json:"DataLen"`
}

type downloadFileReq struct {
	Wxid     string             `json:"Wxid,omitempty"`
	AppID    string             `json:"AppID,omitempty"`
	AttachId string             `json:"AttachId"`
	DataLen  int64              `json:"DataLen"`
	Section  downloadSectionReq `json:"Section"`
	UserName string             `json:"UserName,omitempty"`
}

type cdnDownloadImageReq struct {
	Wxid       string `json:"Wxid"`
	FileAesKey string `json:"FileAesKey"`
	FileNo     string `json:"FileNo"`
}

type fileMessageDownloadReq struct {
	Wxid           string `json:"Wxid"`
	Talker         string `json:"Talker,omitempty"`
	SenderUserName string `json:"SenderUserName,omitempty"`
	MsgID          int64  `json:"MsgID,omitempty"`
	NewMsgID       int64  `json:"NewMsgID,omitempty"`
	MsgSeq         int64  `json:"MsgSeq,omitempty"`
	MsgType        int    `json:"MsgType,omitempty"`
	SourceXml      string `json:"SourceXml,omitempty"`
	Xml            string `json:"Xml,omitempty"`
}

type videoMessageDownloadReq struct {
	Wxid         string             `json:"Wxid"`
	ToWxid       string             `json:"ToWxid"`
	MsgID        int64              `json:"MsgId"`
	DataLen      int64              `json:"DataLen"`
	Section      downloadSectionReq `json:"Section"`
	CompressType int                `json:"CompressType"`

	NewMsgID       int64  `json:"-"`
	SenderUserName string `json:"-"`
}

type voiceMessageDownloadReq struct {
	Wxid         string `json:"Wxid"`
	FromUserName string `json:"FromUserName"`
	MsgID        int64  `json:"MsgId"`
	Length       int64  `json:"Length"`
	BufID        string `json:"Bufid"`
}

type voiceDownloadData struct {
	Buffer string `json:"buffer"`
}

type voiceDownloadResp struct {
	Data voiceDownloadData `json:"data"`
}

type sendVoiceReq struct {
	Wxid      string `json:"Wxid"`
	ToWxid    string `json:"ToWxid"`
	Base64    string `json:"Base64"`
	Type      int    `json:"Type"`
	VoiceTime int    `json:"VoiceTime"`
}

// sendEmojiReq -> /Msg/SendEmoji
// The backend only accepts GIF emoji payloads.
type sendEmojiReq struct {
	Wxid     string `json:"Wxid"`
	ToWxid   string `json:"ToWxid"`
	Md5      string `json:"Md5"`
	Base64   string `json:"Base64"`
	TotalLen int    `json:"TotalLen"`
}

// revokeReq -> /Msg/Revoke
type revokeReq struct {
	Wxid        string `json:"Wxid"`
	ToWxid      string `json:"ToWxid"`
	ClientMsgId int64  `json:"ClientMsgId"`
	NewMsgId    int64  `json:"NewMsgId"`
	CreateTime  int64  `json:"CreateTime"`
}

// --- QueryLocal ---

type queryLocalReq struct {
	Wxid            string `json:"Wxid"`
	Talker          string `json:"Talker,omitempty"`
	SenderUserName  string `json:"SenderUserName,omitempty"`
	MsgType         int    `json:"MsgType,omitempty"`
	Keyword         string `json:"Keyword,omitempty"`
	StartCreateTime int64  `json:"StartCreateTime,omitempty"`
	EndCreateTime   int64  `json:"EndCreateTime,omitempty"`
	Limit           int64  `json:"Limit"`
	Offset          int64  `json:"Offset"`
	Desc            bool   `json:"Desc"`
}

type queryLocalResp struct {
	Total int64          `json:"total"`
	List  []localMessage `json:"list"`
}

type localMessage struct {
	MsgID          int64  `json:"MsgID"`
	NewMsgID       int64  `json:"NewMsgID"`
	MsgSeq         int64  `json:"MsgSeq"`
	MsgType        int    `json:"MsgType"`
	MsgTypeName    string `json:"MsgTypeName"`
	FromUserName   string `json:"FromUserName"`
	ToUserName     string `json:"ToUserName"`
	Talker         string `json:"Talker"`
	SenderUserName string `json:"SenderUserName"`
	IsSendBySelf   bool   `json:"IsSendBySelf"`
	IsGroup        bool   `json:"IsGroup"`
	Content        string `json:"Content"`
	ContentText    string `json:"ContentText"`
	PushContent    string `json:"PushContent"`
	MsgSource      string `json:"MsgSource"`
	Status         int    `json:"Status"`
	ImgStatus      int    `json:"ImgStatus"`
	CreateTime     int64  `json:"CreateTime"`
}

// --- Sync ---

// syncReq -> Msg.SyncParam
type syncReq struct {
	Wxid    string `json:"Wxid"`
	Scene   int    `json:"Scene"`
	Synckey string `json:"Synckey"`
}

// syncStringDoc -> Msg.SyncStringDoc
type syncStringDoc struct {
	Str string `json:"Str,omitempty"`
}

func (s *syncStringDoc) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err == nil {
		s.Str = raw
		return nil
	}
	var obj struct {
		Str    string `json:"Str"`
		String string `json:"string"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if obj.Str != "" {
		s.Str = obj.Str
	} else {
		s.Str = obj.String
	}
	return nil
}

// rawMessage -> Msg.SyncRawMessageDoc
type rawMessage struct {
	MsgId          int64         `json:"MsgId"`
	MsgID          int64         `json:"MsgID"`
	NewMsgId       int64         `json:"NewMsgId"`
	NewMsgID       int64         `json:"NewMsgID"`
	MsgType        int           `json:"MsgType"`
	MsgSeq         int64         `json:"MsgSeq"`
	MsgSource      string        `json:"MsgSource"`
	Content        syncStringDoc `json:"Content"`
	ContentText    string        `json:"ContentText"`
	FromUserName   syncStringDoc `json:"FromUserName"`
	ToUserName     syncStringDoc `json:"ToUserName"`
	Talker         string        `json:"Talker"`
	SenderUserName string        `json:"SenderUserName"`
	IsSendBySelf   bool          `json:"IsSendBySelf"`
	IsGroup        bool          `json:"IsGroup"`
	PushContent    string        `json:"PushContent"`
	CreateTime     int64         `json:"CreateTime"`
	ImgStatus      int           `json:"ImgStatus"`
	Status         int           `json:"Status"`
}

// syncResponse -> Msg.SyncResponseDoc
type syncResponse struct {
	AddMsgs       []rawMessage `json:"AddMsgs"`
	Messages      []rawMessage `json:"Messages"`
	SyncKey       string       `json:"SyncKey"`
	Continue      int          `json:"Continue"`
	ContinueFlag  int          `json:"ContinueFlag"`
	HasNewMessage bool         `json:"HasNewMessage"`
	MessageCount  int          `json:"MessageCount"`
	KeyBuf        keyBuffer    `json:"KeyBuf"`
}

type keyBuffer struct {
	Buffer string `json:"buffer,omitempty"`
}

// --- Contacts / profile ---

type contactResp struct {
	Wxid       string `json:"UserName"`
	NickName   string `json:"NickName"`
	Remark     string `json:"Remark"`
	HeadImgUrl string `json:"SmallHeadImgUrl"`
	BigHeadImg string `json:"BigHeadImgUrl"`
}
