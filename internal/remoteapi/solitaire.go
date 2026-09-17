package remoteapi

import "context"

// SolitaireSource identifies one exact received version of a group sign-up
// chain. Reference is opaque backend data, not XML. Account and Chat prevent
// accidentally applying a reference in a different login or conversation.
type SolitaireSource struct {
	Account   UserID `json:"account"`
	Chat      ChatID `json:"chat"`
	Reference string `json:"reference"`
}

// SolitaireResult confirms one sent version. Message is the complete chain
// returned by the backend, with a fresh source reference, when available. A nil
// Message means the send was confirmed but its content could not be recovered;
// callers must not manufacture a chain or retry the send to retrieve it.
type SolitaireResult struct {
	ID      MessageID
	Message *InboundMessage
}

// SolitaireAPI is an optional capability for appending one entry to a selected
// chain. It must not guess a newer version or retry an uncertain send result.
// The result and normal inbound events may describe the same sent message.
type SolitaireAPI interface {
	JoinSolitaire(ctx context.Context, source SolitaireSource, content string) (SolitaireResult, error)
}
