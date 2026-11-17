package mailer

import (
	"log"
	"net/smtp"
)

type Mailer struct{ Host, Port, User, Pass, From string }

// Send emails via SMTP. With no SMTP_HOST set, it logs the message instead (development).
func (m *Mailer) Send(to, subject, body string) error {
	if m.Host == "" {
		log.Printf("[mail:dev] to=%s subject=%q\n%s", to, subject, body)
		return nil
	}
	msg := "From: " + m.From + "\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	auth := smtp.PlainAuth("", m.User, m.Pass, m.Host)
	return smtp.SendMail(m.Host+":"+m.Port, auth, m.From, []string{to}, []byte(msg))
}
