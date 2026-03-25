package csuite

import (
	"time"

	"github.com/google/uuid"
)

// CsuiteAgent tracks a monitored agent in the C-Suite dashboard.
type CsuiteAgent struct {
	ID              uuid.UUID      `gorm:"type:text;primaryKey"`
	Name            string         `gorm:"uniqueIndex;not null"`
	Status          AgentMonStatus `gorm:"not null;default:offline"`
	HeartbeatAt     *time.Time
	ContextPercent  int    `gorm:"default:0"`
	CurrentActivity string `gorm:"default:''"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CsuiteInboxMessage tracks messages between agents in the C-Suite dashboard.
type CsuiteInboxMessage struct {
	ID        uuid.UUID `gorm:"type:text;primaryKey"`
	FromAgent string    `gorm:"not null"`
	ToAgent   string    `gorm:"not null"`
	Subject   string    `gorm:"not null"`
	Body      string
	Priority  InboxPriority    `gorm:"not null;default:normal"`
	Type      InboxMessageType `gorm:"not null;default:status"`
	Archived  bool             `gorm:"default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
