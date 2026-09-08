package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func normalizeImport(input ImportInput, now time.Time) (Entry, string, error) {
	entry := Entry{
		Label:     strings.TrimSpace(input.Label),
		Issuer:    strings.TrimSpace(input.Issuer),
		Account:   strings.TrimSpace(input.Account),
		Algorithm: strings.ToUpper(strings.TrimSpace(input.Algorithm)),
		Digits:    input.Digits,
		Period:    input.Period,
		CreatedAt: now,
		UpdatedAt: now,
	}
	secret := strings.TrimSpace(input.Secret)
	if uri := strings.TrimSpace(input.URI); uri != "" {
		parsed, parsedSecret, err := parseURI(uri, now)
		if err != nil {
			return Entry{}, "", err
		}
		entry = mergeExplicitEntry(parsed, entry)
		secret = parsedSecret
	}
	if entry.Label == "" {
		entry.Label = entry.Account
	}
	if entry.Label == "" {
		entry.Label = entry.Issuer
	}
	if entry.Label == "" {
		return Entry{}, "", errors.New("TOTP label is required")
	}
	if entry.Algorithm == "" {
		entry.Algorithm = defaultAlgorithm
	}
	if entry.Digits == 0 {
		entry.Digits = defaultDigits
	}
	if entry.Period == 0 {
		entry.Period = defaultPeriod
	}
	secret, err := normalizeSecret(secret)
	if err != nil {
		return Entry{}, "", err
	}
	if err := validateEntry(entry); err != nil {
		return Entry{}, "", err
	}
	return entry, secret, nil
}

func parseURI(value string, now time.Time) (Entry, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "otpauth") || !strings.EqualFold(parsed.Host, "totp") {
		return Entry{}, "", errors.New("TOTP URI must use otpauth://totp")
	}
	if parsed.User != nil || parsed.RawQuery == "" || strings.ContainsAny(value, "\r\n\x00") {
		return Entry{}, "", errors.New("TOTP URI is invalid")
	}
	label, issuerFromLabel, account := parseLabel(parsed.Path)
	query := parsed.Query()
	issuer := strings.TrimSpace(query.Get("issuer"))
	if issuer == "" {
		issuer = issuerFromLabel
	}
	if issuerFromLabel != "" && issuer != "" && !strings.EqualFold(issuerFromLabel, issuer) {
		return Entry{}, "", errors.New("TOTP URI issuer does not match its label")
	}
	digits, err := parseOptionalInt(query.Get("digits"), defaultDigits)
	if err != nil {
		return Entry{}, "", errors.New("TOTP URI digits are invalid")
	}
	period, err := parseOptionalInt(query.Get("period"), defaultPeriod)
	if err != nil {
		return Entry{}, "", errors.New("TOTP URI period is invalid")
	}
	return Entry{
		Label: label, Issuer: issuer, Account: account,
		Algorithm: strings.ToUpper(strings.TrimSpace(query.Get("algorithm"))),
		Digits:    digits, Period: period, CreatedAt: now, UpdatedAt: now,
	}, query.Get("secret"), nil
}

func mergeExplicitEntry(base, explicit Entry) Entry {
	for _, pair := range []struct {
		value *string
		next  string
	}{
		{&base.Label, explicit.Label}, {&base.Issuer, explicit.Issuer}, {&base.Account, explicit.Account}, {&base.Algorithm, explicit.Algorithm},
	} {
		if strings.TrimSpace(pair.next) != "" {
			*pair.value = pair.next
		}
	}
	if explicit.Digits != 0 {
		base.Digits = explicit.Digits
	}
	if explicit.Period != 0 {
		base.Period = explicit.Period
	}
	return base
}

func parseLabel(path string) (label, issuer, account string) {
	label, _ = url.PathUnescape(strings.TrimPrefix(path, "/"))
	label = strings.TrimSpace(label)
	if separator := strings.Index(label, ":"); separator >= 0 {
		issuer = strings.TrimSpace(label[:separator])
		account = strings.TrimSpace(label[separator+1:])
		return label, issuer, account
	}
	return label, "", label
}

func parseOptionalInt(value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func normalizeSecret(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.NewReplacer(" ", "", "-", "", "=", "").Replace(value)
	if value == "" || len(value) > 2048 {
		return "", errors.New("TOTP secret is invalid")
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(decoded) < 10 || len(decoded) > 1024 {
		return "", errors.New("TOTP secret must be a valid Base32 value")
	}
	return value, nil
}

func validateEntry(entry Entry) error {
	if !validID(entry.ID) && entry.ID != "" {
		return errors.New("invalid TOTP entry ID")
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"label", entry.Label, 200}, {"issuer", entry.Issuer, 200}, {"account", entry.Account, 200},
	} {
		if strings.TrimSpace(field.value) == "" && field.name == "label" || len(field.value) > field.limit || strings.ContainsAny(field.value, "\r\n\x00") {
			return fmt.Errorf("invalid TOTP %s", field.name)
		}
	}
	switch strings.ToUpper(entry.Algorithm) {
	case "SHA1", "SHA256", "SHA512":
	default:
		return errors.New("TOTP algorithm must be SHA1, SHA256, or SHA512")
	}
	if entry.Digits != 6 && entry.Digits != 8 {
		return errors.New("TOTP digits must be 6 or 8")
	}
	if entry.Period < 15 || entry.Period > 120 {
		return errors.New("TOTP period must be between 15 and 120 seconds")
	}
	return nil
}

func generate(entry Entry, secret string, at time.Time) (Code, error) {
	if err := validateEntry(entry); err != nil {
		return Code{}, err
	}
	secret, err := normalizeSecret(secret)
	if err != nil {
		return Code{}, err
	}
	decoded, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	counter := uint64(at.UTC().Unix() / int64(entry.Period))
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, counter)
	mac := hmac.New(hashFor(entry.Algorithm), decoded)
	_, _ = mac.Write(message)
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 0x0f)
	if offset+4 > len(sum) {
		return Code{}, errors.New("TOTP algorithm output is invalid")
	}
	value := (uint32(sum[offset])&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	modulus := uint32(math.Pow10(entry.Digits))
	code := fmt.Sprintf("%0*d", entry.Digits, value%modulus)
	validFrom := time.Unix((at.UTC().Unix()/int64(entry.Period))*int64(entry.Period), 0).UTC()
	return Code{EntryID: entry.ID, Value: code, ValidFrom: validFrom, ValidUntil: validFrom.Add(time.Duration(entry.Period) * time.Second)}, nil
}

func hashFor(algorithm string) func() hash.Hash {
	switch strings.ToUpper(algorithm) {
	case "SHA256":
		return sha256.New
	case "SHA512":
		return sha512.New
	default:
		return sha1.New
	}
}
