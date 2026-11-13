package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// The mailbox: messages between agents and people, and the contacts that let
// parties of different owners write to each other. A message stays without a
// delivered_at until it reached its recipient's machine.

const mailboxSchema = `
CREATE TABLE IF NOT EXISTS mail_messages (
	id TEXT PRIMARY KEY,
	pair TEXT NOT NULL,
	from_address TEXT NOT NULL,
	from_name TEXT,
	to_address TEXT NOT NULL,
	to_name TEXT,
	body TEXT NOT NULL,
	created_at TEXT NOT NULL,
	delivered_at TEXT,
	read_at TEXT,
	attempts INTEGER NOT NULL DEFAULT 0,
	last_error TEXT,
	failed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_mail_messages_pair ON mail_messages(pair, created_at);
CREATE INDEX IF NOT EXISTS idx_mail_messages_created ON mail_messages(created_at);
CREATE TABLE IF NOT EXISTS mail_contacts (
	id TEXT PRIMARY KEY,
	pair TEXT NOT NULL UNIQUE,
	a_address TEXT NOT NULL,
	a_name TEXT,
	b_address TEXT NOT NULL,
	b_name TEXT,
	status TEXT NOT NULL,
	requested_by TEXT,
	waiting_for TEXT,
	created_at TEXT NOT NULL,
	accepted_at TEXT
);
`

const mailColumns = `id,from_address,from_name,to_address,to_name,body,created_at,delivered_at,read_at,attempts,last_error,failed_at`

// InsertMailMessage stores a message once. It reports false when a message
// with this id is already stored, which makes redelivery harmless.
func (s *Store) InsertMailMessage(ctx context.Context, message core.MailMessage) (bool, error) {
	result, err := s.writer.ExecContext(ctx, `INSERT INTO mail_messages
		(id,pair,from_address,from_name,to_address,to_name,body,created_at,delivered_at,read_at,attempts,last_error)
		VALUES (?,?,?,?,?,?,?,?,?,?,0,NULL) ON CONFLICT(id) DO NOTHING`,
		message.ID, core.MailPair(message.From, message.To), message.From, nullableString(message.FromName),
		message.To, nullableString(message.ToName), message.Text, formatControlTime(message.CreatedAt),
		nullMailTime(message.DeliveredAt), nullMailTime(message.ReadAt))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// GetMailMessage returns one message or (nil, nil).
func (s *Store) GetMailMessage(ctx context.Context, id string) (*core.MailMessage, error) {
	message, err := scanMail(s.db.QueryRowContext(ctx, `SELECT `+mailColumns+` FROM mail_messages WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &message, nil
}

// ListMailConversation returns the newest messages between two addresses,
// oldest first.
func (s *Store) ListMailConversation(ctx context.Context, a, b string, limit int) ([]core.MailMessage, error) {
	messages, err := s.listMail(ctx, `SELECT `+mailColumns+` FROM mail_messages WHERE pair=? ORDER BY created_at DESC, id DESC LIMIT ?`, core.MailPair(a, b), limit)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages, nil
}

// ListRecentMail returns the newest messages of every conversation, newest
// first.
func (s *Store) ListRecentMail(ctx context.Context, limit int) ([]core.MailMessage, error) {
	return s.listMail(ctx, `SELECT `+mailColumns+` FROM mail_messages ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
}

// ListUndeliveredMail returns the messages still waiting to reach their
// recipient's machine, oldest first.
func (s *Store) ListUndeliveredMail(ctx context.Context, limit int) ([]core.MailMessage, error) {
	return s.listMail(ctx, `SELECT `+mailColumns+` FROM mail_messages WHERE delivered_at IS NULL AND failed_at IS NULL ORDER BY created_at, id LIMIT ?`, limit)
}

// CountMailSince counts the messages between two addresses since a moment.
func (s *Store) CountMailSince(ctx context.Context, a, b string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_messages WHERE pair=? AND created_at>=?`,
		core.MailPair(a, b), formatControlTime(since)).Scan(&count)
	return count, err
}

// MarkMailDelivered records that a message reached its recipient's machine.
func (s *Store) MarkMailDelivered(ctx context.Context, id string, at time.Time) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE mail_messages SET delivered_at=?, last_error=NULL WHERE id=? AND delivered_at IS NULL`,
		formatControlTime(at), id)
	return err
}

// MarkMailFailed gives up on a message that cannot be delivered.
func (s *Store) MarkMailFailed(ctx context.Context, id, problem string, at time.Time) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE mail_messages SET failed_at=?, last_error=? WHERE id=? AND delivered_at IS NULL`,
		formatControlTime(at), problem, id)
	return err
}

// HasMailFrom reports whether one address ever wrote to another.
func (s *Store) HasMailFrom(ctx context.Context, from, to string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mail_messages WHERE from_address=? AND to_address=? AND failed_at IS NULL)`, from, to).Scan(&found)
	return found, err
}

// HasMailSender reports whether an address ever sent a message.
func (s *Store) HasMailSender(ctx context.Context, address string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mail_messages WHERE from_address=?)`, address).Scan(&found)
	return found, err
}

// RecordMailAttempt records a delivery that did not get through.
func (s *Store) RecordMailAttempt(ctx context.Context, id, problem string) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE mail_messages SET attempts=attempts+1, last_error=? WHERE id=?`, problem, id)
	return err
}

// MarkMailRead marks what one address received from another as read.
func (s *Store) MarkMailRead(ctx context.Context, reader, from string, at time.Time) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE mail_messages SET read_at=? WHERE to_address=? AND from_address=? AND read_at IS NULL`,
		formatControlTime(at), reader, from)
	return err
}

func (s *Store) listMail(ctx context.Context, query string, args ...any) ([]core.MailMessage, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.MailMessage{}
	for rows.Next() {
		message, err := scanMail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func scanMail(row interface{ Scan(...any) error }) (core.MailMessage, error) {
	var message core.MailMessage
	var fromName, toName, delivered, read, problem, failed sql.NullString
	var created string
	if err := row.Scan(&message.ID, &message.From, &fromName, &message.To, &toName, &message.Text, &created,
		&delivered, &read, &message.Attempts, &problem, &failed); err != nil {
		return core.MailMessage{}, err
	}
	message.FailedAt = mailTime(failed)
	message.FromName, message.ToName, message.Error = fromName.String, toName.String, problem.String
	message.CreatedAt = parseControlTime(created)
	message.DeliveredAt, message.ReadAt = mailTime(delivered), mailTime(read)
	return message, nil
}

const mailContactColumns = `id,a_address,a_name,b_address,b_name,status,requested_by,waiting_for,created_at,accepted_at`

// UpsertMailContact saves the contact between two addresses. There is one
// contact per pair: saving it again replaces its names and state.
func (s *Store) UpsertMailContact(ctx context.Context, contact core.MailContact) error {
	_, err := s.writer.ExecContext(ctx, `INSERT INTO mail_contacts
		(id,pair,a_address,a_name,b_address,b_name,status,requested_by,waiting_for,created_at,accepted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(pair) DO UPDATE SET a_name=excluded.a_name, b_name=excluded.b_name, status=excluded.status,
			requested_by=excluded.requested_by, waiting_for=excluded.waiting_for, accepted_at=excluded.accepted_at`,
		contact.ID, core.MailPair(contact.A, contact.B), contact.A, nullableString(contact.AName), contact.B, nullableString(contact.BName),
		contact.Status, nullableString(contact.RequestedBy), nullableString(contact.WaitingFor), formatControlTime(contact.CreatedAt), nullMailTime(contact.AcceptedAt))
	return err
}

// GetMailContact returns the contact between two addresses or (nil, nil).
func (s *Store) GetMailContact(ctx context.Context, a, b string) (*core.MailContact, error) {
	return s.oneMailContact(ctx, `SELECT `+mailContactColumns+` FROM mail_contacts WHERE pair=?`, core.MailPair(a, b))
}

// GetMailContactByID returns one contact or (nil, nil).
func (s *Store) GetMailContactByID(ctx context.Context, id string) (*core.MailContact, error) {
	return s.oneMailContact(ctx, `SELECT `+mailContactColumns+` FROM mail_contacts WHERE id=?`, id)
}

func (s *Store) oneMailContact(ctx context.Context, query, arg string) (*core.MailContact, error) {
	contact, err := scanMailContact(s.db.QueryRowContext(ctx, query, arg))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &contact, nil
}

// ListMailContacts returns every contact, oldest first.
func (s *Store) ListMailContacts(ctx context.Context) ([]core.MailContact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mailContactColumns+` FROM mail_contacts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.MailContact{}
	for rows.Next() {
		contact, err := scanMailContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, contact)
	}
	return out, rows.Err()
}

// DeleteMailContact removes a contact. Messages already sent stay.
func (s *Store) DeleteMailContact(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM mail_contacts WHERE id=?`, id)
	return err
}

func scanMailContact(row interface{ Scan(...any) error }) (core.MailContact, error) {
	var contact core.MailContact
	var aName, bName, requestedBy, waitingFor, accepted sql.NullString
	var created string
	if err := row.Scan(&contact.ID, &contact.A, &aName, &contact.B, &bName, &contact.Status, &requestedBy, &waitingFor, &created, &accepted); err != nil {
		return core.MailContact{}, err
	}
	contact.AName, contact.BName, contact.RequestedBy, contact.WaitingFor = aName.String, bName.String, requestedBy.String, waitingFor.String
	contact.CreatedAt, contact.AcceptedAt = parseControlTime(created), mailTime(accepted)
	return contact, nil
}

func nullMailTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return formatControlTime(*value)
}

func mailTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := parseControlTime(value.String)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}
