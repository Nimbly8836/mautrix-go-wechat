package wechat

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Only the two documented ack shapes are accepted. In particular, never look
// recursively for IDs in arbitrary data, quoted messages or history records.
func (r *sendResp) UnmarshalJSON(data []byte) error {
	*r = sendResp{}
	type ack struct {
		NewMsgID    *uint64         `json:"NewMsgId"`
		MsgID       *uint64         `json:"MsgId"`
		ClientMsgID json.RawMessage `json:"ClientMsgId"`
		CreateTime  int64           `json:"CreateTime"`
		Ret         *int64          `json:"Ret"`
	}
	var wire struct {
		ack
		BaseResponse json.RawMessage `json:"BaseResponse"`
		Count        json.RawMessage `json:"Count"`
		List         json.RawMessage `json:"List"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("invalid send acknowledgement: %w", err)
	}
	hasBase := len(wire.BaseResponse) != 0
	if hasBase {
		var base struct {
			Ret *int64 `json:"ret"`
		}
		if err := json.Unmarshal(wire.BaseResponse, &base); err != nil {
			return fmt.Errorf("invalid send BaseResponse: %w", err)
		}
		if base.Ret == nil {
			return fmt.Errorf("send BaseResponse lacks a return code")
		}
		if *base.Ret != 0 {
			return fmt.Errorf("微信拒绝发送：BaseResponse.ret=%d", *base.Ret)
		}
	}
	item := wire.ack
	if len(wire.List) != 0 || len(wire.Count) != 0 {
		var count *int32
		var list []*ack
		if err := json.Unmarshal(wire.Count, &count); err != nil {
			return fmt.Errorf("invalid send Count: %w", err)
		}
		if err := json.Unmarshal(wire.List, &list); err != nil {
			return fmt.Errorf("invalid send List: %w", err)
		}
		if !hasBase || count == nil || *count != 1 || len(list) != 1 || list[0] == nil {
			return fmt.Errorf("send acknowledgement requires one confirmed List item and Count=1")
		}
		if wire.MsgID != nil || wire.NewMsgID != nil {
			return fmt.Errorf("ambiguous flat and List send acknowledgement")
		}
		item = *list[0]
		if item.Ret == nil {
			return fmt.Errorf("send List item lacks a return code")
		}
	}
	if item.Ret != nil && *item.Ret != 0 {
		return fmt.Errorf("微信拒绝发送：Ret=%d", *item.Ret)
	}
	// A legacy endpoint may return only one of the two ID namespaces. Neither
	// client IDs nor timestamps count as server acknowledgement IDs.
	var decoded sendResp
	if item.NewMsgID != nil {
		decoded.NewMsgId = *item.NewMsgID
	}
	if item.MsgID != nil {
		decoded.MsgId = *item.MsgID
	}
	if err := decoded.validateSendResponse(); err != nil {
		return err
	}
	if len(item.ClientMsgID) > 0 && string(item.ClientMsgID) != "null" {
		if item.ClientMsgID[0] == '"' {
			if err := json.Unmarshal(item.ClientMsgID, &decoded.ClientMsgId); err != nil {
				return err
			}
		} else {
			var id uint64
			if err := json.Unmarshal(item.ClientMsgID, &id); err != nil {
				return fmt.Errorf("invalid send client ID: %w", err)
			}
			decoded.ClientMsgId = strconv.FormatUint(id, 10)
		}
	}
	decoded.CreateTime = item.CreateTime
	decoded.baseResponseValidated = hasBase
	*r = decoded
	return nil
}

// transport invokes this even when Data is absent (UnmarshalJSON would not run).
// Errors deliberately aren't APIError: uncertain acks must never trigger retry.
func (r *sendResp) validateSendResponse() error {
	if r.NewMsgId == 0 && r.MsgId == 0 {
		return fmt.Errorf("send acknowledgement has no positive server message ID; outcome uncertain, do not resend automatically")
	}
	return nil
}
