package entities

import (
	"errors"
	"regexp"
	"time"
)

const (
	MaxContentLength = 160
)

var (
	ErrEmptyContent       = errors.New("content cannot be empty")
	ErrContentTooLong     = errors.New("content exceeds maximum length")
	ErrMissingPhone       = errors.New("recipient phone required")
	ErrInvalidPhoneFormat = errors.New("invalid phone number format")
)

var phoneRegex = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

type Message struct {
	ID             int       `json:"id"`
	RecipientPhone string    `json:"recipient_phone"`
	Content        string    `json:"content"`
	SentStatus     bool      `json:"sent_status"`
	SentAt         time.Time `json:"sent_at,omitempty"`
}

func (m *Message) Validate() error {
	if len(m.Content) == 0 {
		return ErrEmptyContent
	}
	if len(m.Content) > MaxContentLength {
		return ErrContentTooLong
	}
	if m.RecipientPhone == "" {
		return ErrMissingPhone
	}
	if !phoneRegex.MatchString(m.RecipientPhone) {
		return ErrInvalidPhoneFormat
	}
	return nil
}
