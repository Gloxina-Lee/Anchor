package anchor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.etcd.io/bbolt"
)

var (
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

var (
	metaBucket     = []byte("meta")
	linksBucket    = []byte("links")
	slugsBucket    = []byte("slugs")
	usedBucket     = []byte("used")
	sessionsBucket = []byte("sessions")
	settingsKey    = []byte("settings")
	accountKey     = []byte("account")
)

type Store struct{ db *bbolt.DB }

type account struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
}

type session struct {
	CSRF      string    `json:"csrf"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func OpenStore(path string) (*Store, error) {
	db, err := bbolt.Open(path, 0600, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{metaBucket, linksBucket, slugsBucket, usedBucket, sessionsBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(metaBucket)
		if meta.Get(settingsKey) == nil {
			return putJSON(meta, settingsKey, defaultSettings())
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Account() (account, bool, error) {
	var a account
	var found bool
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket(metaBucket).Get(accountKey)
		if data == nil {
			return nil
		}
		found = true
		return json.Unmarshal(data, &a)
	})
	return a, found, err
}

func (s *Store) CreateAccount(username, hash string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta.Get(accountKey) != nil {
			return ErrConflict
		}
		return putJSON(meta, accountKey, account{Username: username, Hash: hash})
	})
}

func (s *Store) Settings() (Settings, error) {
	var settings Settings
	err := s.db.View(func(tx *bbolt.Tx) error {
		return json.Unmarshal(tx.Bucket(metaBucket).Get(settingsKey), &settings)
	})
	if settings.RootBehavior == "" {
		settings.RootBehavior = "admin"
	}
	return settings, err
}

func (s *Store) UpdateSettings(settings Settings) error {
	if settings.RootBehavior == "" {
		settings.RootBehavior = "admin"
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return putJSON(tx.Bucket(metaBucket), settingsKey, settings)
	})
}

func (s *Store) CreateLink(link Link, requested string) (Link, error) {
	settings, err := s.Settings()
	if err != nil {
		return Link{}, err
	}
	link.ID, err = randomHex(16)
	if err != nil {
		return Link{}, err
	}
	err = s.db.Update(func(tx *bbolt.Tx) error {
		if requested != "" {
			link.Code = requested
			return insertLink(tx, &link, settings.ReuseCodes)
		}
		for length := settings.MinLength; length <= settings.MaxLength; length++ {
			for attempt := 0; attempt < 12; attempt++ {
				code, err := randomCode(length, settings.ExcludeSimilar)
				if err != nil {
					return err
				}
				if !validCode(code, settings.MaxLength) {
					continue
				}
				link.Code = code
				err = insertLink(tx, &link, settings.ReuseCodes)
				if errors.Is(err, ErrConflict) {
					continue
				}
				return err
			}
		}
		return fmt.Errorf("%w: no available short code", ErrConflict)
	})
	if err != nil {
		return Link{}, err
	}
	link.setStatus(time.Now())
	return link, nil
}

func insertLink(tx *bbolt.Tx, link *Link, reuse bool) error {
	slugs := tx.Bucket(slugsBucket)
	links := tx.Bucket(linksBucket)
	used := tx.Bucket(usedBucket)
	key := []byte(link.Code)
	if !reuse && used.Get(key) != nil {
		return ErrConflict
	}
	if previousID := slugs.Get(key); previousID != nil {
		var previous Link
		if err := json.Unmarshal(links.Get(previousID), &previous); err != nil {
			return err
		}
		if previous.ExpiresAt == nil || time.Now().Before(*previous.ExpiresAt) {
			return ErrConflict
		}
		now := time.Now().UTC()
		previous.SupersededAt = &now
		if err := putJSON(links, previousID, previous); err != nil {
			return err
		}
	}
	if err := putJSON(links, []byte(link.ID), link); err != nil {
		return err
	}
	if err := slugs.Put(key, []byte(link.ID)); err != nil {
		return err
	}
	return used.Put(key, []byte{1})
}

func (s *Store) ListLinks() ([]Link, error) {
	links := []Link{}
	now := time.Now()
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(linksBucket).ForEach(func(_, data []byte) error {
			var link Link
			if err := json.Unmarshal(data, &link); err != nil {
				return err
			}
			if link.DeletedAt == nil && link.SupersededAt == nil {
				link.setStatus(now)
				links = append(links, link)
			}
			return nil
		})
	})
	sort.Slice(links, func(i, j int) bool { return links[i].CreatedAt.After(links[j].CreatedAt) })
	return links, err
}

func (s *Store) ActiveLink(code string) (Link, error) {
	var link Link
	err := s.db.View(func(tx *bbolt.Tx) error {
		id := tx.Bucket(slugsBucket).Get([]byte(code))
		if id == nil {
			return ErrNotFound
		}
		if err := json.Unmarshal(tx.Bucket(linksBucket).Get(id), &link); err != nil {
			return err
		}
		link.setStatus(time.Now())
		if link.Status != "active" {
			return ErrNotFound
		}
		return nil
	})
	return link, err
}

func (s *Store) UpdateExpiry(ids []string, expiry *time.Time) error {
	return s.changeLinks(ids, func(tx *bbolt.Tx, links []Link) error {
		for _, link := range links {
			if expiry != nil && !expiry.After(link.StartsAt) {
				return fmt.Errorf("%w: expiry must follow activation", ErrConflict)
			}
			link.ExpiresAt = expiry
			if err := putJSON(tx.Bucket(linksBucket), []byte(link.ID), link); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) UpdateNote(id, note string) error {
	return s.changeLinks([]string{id}, func(tx *bbolt.Tx, links []Link) error {
		link := links[0]
		link.Note = note
		return putJSON(tx.Bucket(linksBucket), []byte(link.ID), link)
	})
}

func (s *Store) DeleteLinks(ids []string) error {
	return s.changeLinks(ids, func(tx *bbolt.Tx, links []Link) error {
		now := time.Now().UTC()
		for _, link := range links {
			link.DeletedAt = &now
			if err := putJSON(tx.Bucket(linksBucket), []byte(link.ID), link); err != nil {
				return err
			}
			if err := tx.Bucket(slugsBucket).Delete([]byte(link.Code)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) changeLinks(ids []string, change func(*bbolt.Tx, []Link) error) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		links := make([]Link, 0, len(ids))
		for _, id := range ids {
			data := tx.Bucket(linksBucket).Get([]byte(id))
			if data == nil {
				return ErrNotFound
			}
			var link Link
			if err := json.Unmarshal(data, &link); err != nil {
				return err
			}
			if link.DeletedAt != nil || link.SupersededAt != nil || string(tx.Bucket(slugsBucket).Get([]byte(link.Code))) != id {
				return ErrNotFound
			}
			links = append(links, link)
		}
		return change(tx, links)
	})
}

func (s *Store) PutSession(token string, value session) error {
	key := sessionKey(token)
	return s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(sessionsBucket)
		var expired [][]byte
		if err := bucket.ForEach(func(k, data []byte) error {
			var stored session
			if json.Unmarshal(data, &stored) == nil && !time.Now().Before(stored.ExpiresAt) {
				expired = append(expired, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, old := range expired {
			if err := bucket.Delete(old); err != nil {
				return err
			}
		}
		return putJSON(bucket, key, value)
	})
}

func (s *Store) Session(token string) (session, bool, error) {
	var value session
	var found bool
	err := s.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket(sessionsBucket).Get(sessionKey(token))
		if data == nil {
			return nil
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		found = time.Now().Before(value.ExpiresAt)
		return nil
	})
	return value, found, err
}

func (s *Store) DeleteSession(token string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(sessionsBucket).Delete(sessionKey(token))
	})
}

func sessionKey(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func randomHex(count int) (string, error) {
	buf := make([]byte, count)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func putJSON(bucket *bbolt.Bucket, key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put(key, data)
}
