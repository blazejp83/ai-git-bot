package auth

import (
	"database/sql"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AdminService struct {
	db *sql.DB
}

func NewAdminService(db *sql.DB) *AdminService {
	return &AdminService{db: db}
}

func (s *AdminService) IsSetupRequired() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *AdminService) CreateAdmin(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.Exec(
		"INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)",
		username, string(hash), now, now,
	)
	return err
}

func (s *AdminService) Authenticate(username, password string) (*AdminUser, error) {
	var u AdminUser
	err := s.db.QueryRow(
		"SELECT id, username, password_hash, created_at, updated_at FROM admin_users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, nil
	}
	return &u, nil
}
