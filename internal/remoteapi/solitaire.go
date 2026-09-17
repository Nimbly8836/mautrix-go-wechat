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

// SolitaireAPI is an optional capability for appending one entry to a selected
// chain. It must not guess a newer version or retry an uncertain send result.
type SolitaireAPI interface {
	JoinSolitaire(ctx context.Context, source SolitaireSource, content string) (MessageID, error)
}
