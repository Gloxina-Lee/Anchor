package anchor

import "time"

type Settings struct {
	MinLength       int    `json:"minLength"`
	MaxLength       int    `json:"maxLength"`
	ExcludeSimilar  bool   `json:"excludeSimilar"`
	ReuseCodes      bool   `json:"reuseCodes"`
	RootBehavior    string `json:"rootBehavior"`
	RootRedirectURL string `json:"rootRedirectUrl"`
	RootHTML        string `json:"rootHtml"`
}

func defaultSettings() Settings {
	return Settings{MinLength: 6, MaxLength: 32, ReuseCodes: true, RootBehavior: "admin"}
}

type Link struct {
	ID           string     `json:"id"`
	Code         string     `json:"code"`
	Destination  string     `json:"destination"`
	Note         string     `json:"note"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartsAt     time.Time  `json:"startsAt"`
	ExpiresAt    *time.Time `json:"expiresAt"`
	DeletedAt    *time.Time `json:"-"`
	SupersededAt *time.Time `json:"-"`
	Status       string     `json:"status"`
}

func (l *Link) setStatus(now time.Time) {
	switch {
	case l.DeletedAt != nil:
		l.Status = "deleted"
	case l.SupersededAt != nil:
		l.Status = "superseded"
	case l.ExpiresAt != nil && !now.Before(*l.ExpiresAt):
		l.Status = "expired"
	case now.Before(l.StartsAt):
		l.Status = "scheduled"
	default:
		l.Status = "active"
	}
}
