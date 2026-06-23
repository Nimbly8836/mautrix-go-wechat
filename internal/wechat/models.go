package wechat

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
	Uuid    string `json:"Uuid"`
	QRCode  string `json:"QrCode"` // sometimes "QRCodeURL" / base64
	QRBase  string `json:"QrBase64"`
	BaseUrl string `json:"BaseUrl"`
}

// checkQRResp is the Data of /Login/LoginCheckQR.
type checkQRResp struct {
	Status      int    `json:"Status"` // 0=waiting, 1=scanned, 2=confirmed/success
	Wxid        string `json:"Wxid"`
	NickName    string `json:"NickName"`
	HeadImgUrl  string `json:"HeadImgUrl"`
	ExpiredTime int    `json:"ExpiredTime"`
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

// rawMessage -> Msg.SyncRawMessageDoc
type rawMessage struct {
	MsgId        int64         `json:"MsgId"`
	NewMsgId     int64         `json:"NewMsgId"`
	MsgType      int           `json:"MsgType"`
	MsgSeq       int64         `json:"MsgSeq"`
	MsgSource    string        `json:"MsgSource"`
	Content      syncStringDoc `json:"Content"`
	FromUserName syncStringDoc `json:"FromUserName"`
	ToUserName   syncStringDoc `json:"ToUserName"`
	PushContent  string        `json:"PushContent"`
	CreateTime   int64         `json:"CreateTime"`
	ImgStatus    int           `json:"ImgStatus"`
	Status       int           `json:"Status"`
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
