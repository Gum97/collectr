// Package domain holds the data subject rights vocabulary.
//
// It is a bounded context of its own: the language here is the law's, not the
// product's. Nothing in it knows what a form or a link is.
package domain

import (
	"errors"
	"time"
)

// Request types a data subject can raise.
const (
	TypeAccess   = "access"
	TypeRectify  = "rectify"
	TypeErase    = "erase"
	TypeRestrict = "restrict"
	TypeWithdraw = "withdraw"
	TypeExport   = "export"
	TypeObject   = "object"
)

// Request lifecycle.
const (
	StatusReceived   = "received"
	StatusVerified   = "verified"
	StatusInProgress = "in_progress"
	StatusFulfilled  = "fulfilled"
	StatusRejected   = "rejected"
)

// Token scopes.
const (
	// ScopePortal grants access to everything the subject has submitted to one
	// tenant.
	ScopePortal = "portal"
	// ScopeReceipt grants access to a single submission. It is what the
	// respondent gets at submission time, and it is deliberately narrower: a
	// link sitting in someone's inbox should not become a key to their whole
	// history if that inbox is later compromised.
	ScopeReceipt = "receipt"
)

// TokenTTL is how long a magic link stays usable. Short, because it travels
// through email or SMS, neither of which is a confidential channel.
const TokenTTL = 15 * time.Minute

// SessionTTL is how long a verified portal session lasts.
const SessionTTL = 30 * time.Minute

// Errors returned by the DSR module.
var (
	ErrTokenInvalid  = errors.New("token is invalid, expired or already used")
	ErrRateLimited   = errors.New("too many attempts")
	ErrNotFound      = errors.New("not found")
	ErrAlreadyErased = errors.New("this data has already been erased")
	ErrForbidden     = errors.New("this session may not access that record")
)

// Request is one exercise of a right.
type Request struct {
	ID          string
	Type        string
	Status      string
	ReceivedAt  time.Time
	DueAt       time.Time
	FulfilledAt *time.Time
}

// Overdue reports whether the statutory deadline has passed.
func (r Request) Overdue(now time.Time) bool {
	return r.Status != StatusFulfilled && r.Status != StatusRejected && now.After(r.DueAt)
}

// AutoFulfillable reports whether a request can be completed without a human.
//
// Erasure and withdrawal are mechanical: the system knows exactly what to delete
// and what to stop doing. Handling them automatically is not a shortcut -- it is
// how the deadline gets met reliably, on a Sunday, while everyone is on holiday.
// Objection and restriction need judgement and stay in the queue.
func (r Request) AutoFulfillable() bool {
	switch r.Type {
	case TypeErase, TypeWithdraw, TypeAccess, TypeExport:
		return true
	default:
		return false
	}
}

// ValidType reports whether t is a request type the portal accepts.
func ValidType(t string) bool {
	switch t {
	case TypeAccess, TypeRectify, TypeErase, TypeRestrict, TypeWithdraw, TypeExport, TypeObject:
		return true
	default:
		return false
	}
}
