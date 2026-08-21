package tenant

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// CanonicalAppName returns the tRPC-Agent-Go AppName for a tenant application.
func CanonicalAppName(tenantID, appID string) (string, error) {
	tenantSegment, err := canonicalSegment("tenant ID", tenantID)
	if err != nil {
		return "", err
	}
	appSegment, err := canonicalSegment("app ID", appID)
	if err != nil {
		return "", err
	}
	return "tenant/" + tenantSegment + "/app/" + appSegment, nil
}

// CanonicalUserID scopes an external user to one channel binding.
func CanonicalUserID(
	channelType ChannelType,
	bindingID string,
	externalUserID string,
) (string, error) {
	channelSegment, err := canonicalSegment("channel type", string(channelType))
	if err != nil {
		return "", err
	}
	bindingSegment, err := canonicalSegment("binding ID", bindingID)
	if err != nil {
		return "", err
	}
	userSegment, err := canonicalSegment("external user ID", externalUserID)
	if err != nil {
		return "", err
	}
	return channelSegment + "/" + bindingSegment + "/" + userSegment, nil
}

// DirectSessionID returns the canonical direct-message session ID.
func DirectSessionID(bindingID, externalUserID string) (string, error) {
	bindingSegment, err := canonicalSegment("binding ID", bindingID)
	if err != nil {
		return "", err
	}
	userSegment, err := canonicalSegment("external user ID", externalUserID)
	if err != nil {
		return "", err
	}
	return "dm/" + bindingSegment + "/" + userSegment, nil
}

// GroupSessionID returns the canonical group-message session ID.
func GroupSessionID(bindingID, conversationID string) (string, error) {
	bindingSegment, err := canonicalSegment("binding ID", bindingID)
	if err != nil {
		return "", err
	}
	conversationSegment, err := canonicalSegment("conversation ID", conversationID)
	if err != nil {
		return "", err
	}
	return "group/" + bindingSegment + "/" + conversationSegment, nil
}

// ThreadSessionID appends a provider thread or topic to a base session ID.
func ThreadSessionID(baseSessionID, threadID string) (string, error) {
	if strings.TrimSpace(baseSessionID) == "" {
		return "", errors.New("base session ID is required")
	}
	threadSegment, err := canonicalSegment("thread ID", threadID)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(baseSessionID, "/") + "/thread/" + threadSegment, nil
}

func canonicalSegment(name, value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("%s must be non-empty and trimmed", name)
	}
	if len(value) > 128 {
		return "", fmt.Errorf("%s exceeds 128 bytes", name)
	}
	if value == "." || value == ".." {
		return "", fmt.Errorf("%s uses a reserved value", name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", fmt.Errorf("%s contains control characters", name)
		}
	}
	return url.PathEscape(value), nil
}
