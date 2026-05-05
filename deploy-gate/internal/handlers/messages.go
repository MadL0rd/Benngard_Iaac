// Domain-level TG helpers: text builders for approval messages, plus the
// two small interpretations of TG-API specifics we need (UserLabel,
// isNotModified).
package handlers

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"benngard/deploy-gate/internal/approval"
)

// Cached TZ locations — half the team works on MSK, half on Madrid time;
// rendering both in messages saves mental math.
var (
	mskTZ    = mustLoadLocation("Europe/Moscow")
	madridTZ = mustLoadLocation("Europe/Madrid")
)

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("load tz " + name + ": " + err.Error())
	}
	return loc
}

// formatDualTZ renders an instant as "HH:MM:SS MSK / HH:MM:SS CET" —
// the TZ-abbreviation token (MST in Go's reference layout) auto-resolves
// to MSK or CET/CEST depending on date.
func formatDualTZ(t time.Time) string {
	return fmt.Sprintf("%s / %s",
		t.In(mskTZ).Format("15:04:05 MST"),
		t.In(madridTZ).Format("15:04:05 MST"))
}

func UserLabel(user models.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	if user.FirstName != "" {
		return user.FirstName
	}
	return strconv.FormatInt(user.ID, 10)
}

// isNotModified detects Telegram's "message is not modified" no-op error.
// go-telegram/bot wraps 400-class API errors with bot.ErrorBadRequest +
// the TG description appended.
func isNotModified(err error) bool {
	if !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	return strings.Contains(err.Error(), "not modified")
}

// --- text bodies ---

func pendingMessage(pending *approval.Pending) string {
	return fmt.Sprintf(
		"🚀 Deploy approval needed\n"+
			"service: %s\n"+
			"env:     %s\n"+
			"image:   %s\n"+
			"created: %s",
		pending.Service, pending.Env, pending.Image, pending.CreatedAt.Format(time.RFC3339),
	)
}

func deployingMessage(pending *approval.Pending, approver models.User, approvedAt time.Time) string {
	return fmt.Sprintf(
		"⏳ Deploying...\n"+
			"approved by %s (%d) at %s\n"+
			"service: %s\n"+
			"env:     %s\n"+
			"image:   %s",
		UserLabel(approver), approver.ID, formatDualTZ(approvedAt),
		pending.Service, pending.Env, pending.Image,
	)
}

func deniedMessage(pending *approval.Pending, approver models.User) string {
	return fmt.Sprintf(
		"❌ Denied by %s (%d)\n"+
			"service: %s\n"+
			"env:     %s\n"+
			"image:   %s",
		UserLabel(approver), approver.ID,
		pending.Service, pending.Env, pending.Image,
	)
}

func timeoutMessage(pending *approval.Pending) string {
	return fmt.Sprintf(
		"⏱ Timeout, auto-denied\n"+
			"service: %s\n"+
			"env:     %s\n"+
			"image:   %s",
		pending.Service, pending.Env, pending.Image,
	)
}

func doneMessage(pending *approval.Pending, approver models.User, approvedAt, finishedAt time.Time, detail string, failed bool) string {
	icon := "✅"
	status := "deploy ok"
	if failed {
		icon = "🛑"
		status = "deploy FAILED"
	}
	duration := finishedAt.Sub(approvedAt).Round(time.Second)
	return fmt.Sprintf(
		"%s %s\n"+
			"approved by %s at %s\n"+
			"finished at %s (took %s)\n"+
			"service: %s\n"+
			"env:     %s\n"+
			"image:   %s\n"+
			"---\n%s",
		icon, status,
		UserLabel(approver), formatDualTZ(approvedAt),
		formatDualTZ(finishedAt), duration,
		pending.Service, pending.Env, pending.Image, detail,
	)
}

func invalidMessage(service, env, reason, image, remote string) string {
	text := fmt.Sprintf(
		"⚠️ Invalid webhook request\n"+
			"service: %s\n"+
			"env:     %s\n"+
			"reason:  %s",
		service, env, reason)
	if image != "" {
		text += "\nimage:   " + image
	}
	if remote != "" {
		text += "\nremote:  " + remote
	}
	return text
}

func tail(text string, lineCount int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= lineCount {
		return text
	}
	return strings.Join(lines[len(lines)-lineCount:], "\n")
}
