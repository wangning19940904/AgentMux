package superagent

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
)

// An agent's portrait is one of the Console's presets or a small uploaded
// image. The Console shrinks an upload before saving it, so the limit only
// keeps an oversized image out of every agent list.
const maxAvatarBytes = 192 << 10

var avatarPresetPattern = regexp.MustCompile(`^preset:[a-z0-9-]{1,32}$`)

var avatarImageTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

func validAvatar(avatar string) error {
	if avatar == "" || avatarPresetPattern.MatchString(avatar) {
		return nil
	}
	rest, ok := strings.CutPrefix(avatar, "data:")
	if !ok {
		return errors.New(`avatar must be "preset:<id>" or an image data URL`)
	}
	mediaType, data, ok := strings.Cut(rest, ";base64,")
	if !ok || !containsFold(avatarImageTypes, mediaType) {
		return errors.New("avatar must be a PNG, JPEG, WebP or GIF image")
	}
	if len(data) > base64.StdEncoding.EncodedLen(maxAvatarBytes) {
		return errors.New("avatar image is too large; use one under 192 KB")
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return errors.New("avatar image is not valid base64")
	}
	return nil
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}
