package board

import "errors"

var (
	// ErrNotFound means a post, entry, or row does not exist.
	ErrNotFound = errors.New("board: not found")
	// ErrForbidden means the actor is not a participant of the post.
	ErrForbidden = errors.New("board: forbidden")
	// ErrClaimLost means a claim token no longer owns its inbox rows.
	ErrClaimLost = errors.New("board: claim lost")
	// ErrAmbiguous means a reference prefix matches more than one object.
	ErrAmbiguous = errors.New("board: ambiguous reference")
	// ErrInvalid means the input is malformed.
	ErrInvalid = errors.New("board: invalid input")
)
