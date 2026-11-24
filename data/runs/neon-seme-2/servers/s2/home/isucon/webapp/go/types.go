package main

import (
	"crypto/sha256"
	"encoding/hex"
)

// --- Public API JSON types (100% compatible with reference implementation) ---

type InitializeResponse struct {
	Language string `json:"language"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type Theme struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
}

type User struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Theme       Theme  `json:"theme,omitempty"`
	IconHash    string `json:"icon_hash,omitempty"`
}

type PostUserRequestTheme struct {
	DarkMode bool `json:"dark_mode"`
}

type PostUserRequest struct {
	Name        string               `json:"name"`
	DisplayName string               `json:"display_name"`
	Description string               `json:"description"`
	Password    string               `json:"password"`
	Theme       PostUserRequestTheme `json:"theme"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type PostIconRequest struct {
	Image []byte `json:"image"`
}

type PostIconResponse struct {
	ID int64 `json:"id"`
}

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TagsResponse struct {
	Tags []*Tag `json:"tags"`
}

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

type Livecomment struct {
	ID         int64      `json:"id"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	Comment    string     `json:"comment"`
	Tip        int64      `json:"tip"`
	CreatedAt  int64      `json:"created_at"`
}

type LivecommentReport struct {
	ID          int64       `json:"id"`
	Reporter    User        `json:"reporter"`
	Livecomment Livecomment `json:"livecomment"`
	CreatedAt   int64       `json:"created_at"`
}

type ModerateRequest struct {
	NGWord string `json:"ng_word"`
}

type NGWord struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	LivestreamID int64  `json:"livestream_id"`
	Word         string `json:"word"`
	CreatedAt    int64  `json:"created_at"`
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

type Reaction struct {
	ID         int64      `json:"id"`
	EmojiName  string     `json:"emoji_name"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

type PaymentResult struct {
	TotalTip int64 `json:"total_tip"`
}

// --- Internal / Snapshot / Sync types ---

type SnapUser struct {
	ID             int64
	Name           string
	DisplayName    string
	Description    string
	HashedPassword string
	ThemeID        int64
	DarkMode       bool
}

type SnapLivestream struct {
	ID           int64
	UserID       int64
	Title        string
	Description  string
	PlaylistUrl  string
	ThumbnailUrl string
	StartAt      int64
	EndAt        int64
	TagIDs       []int64
}

type SnapLivecomment struct {
	ID           int64
	UserID       int64
	LivestreamID int64
	Comment      string
	Tip          int64
	CreatedAt    int64
}

type SnapReaction struct {
	ID           int64
	UserID       int64
	LivestreamID int64
	EmojiName    string
	CreatedAt    int64
}

type SnapNGWord struct {
	ID           int64
	UserID       int64
	LivestreamID int64
	Word         string
	CreatedAt    int64
}

type InitialSnapshot struct {
	FallbackImage []byte
	FallbackHash  string
	DNSNames      []string
	Tags          []Tag
	Users         []SnapUser
	Livestreams   []SnapLivestream
	Livecomments  []SnapLivecomment
	Reactions     []SnapReaction
	NGWords       []SnapNGWord
	SlotBase      int64
	SlotStep      int64
	Slots         []int64
}

// Internal RPC structs between Edge (s2/s3) and Core (s1)
type InternalRegisterReq struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	HashedPassword string `json:"hashed_password"`
	DarkMode       bool   `json:"dark_mode"`
}

type InternalSetIconReq struct {
	UserID int64  `json:"user_id"`
	Hash   string `json:"hash"`
	Image  []byte `json:"image,omitempty"`
}

type InternalUserAuth struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	HashedPassword string `json:"hashed_password"`
	ThemeID        int64  `json:"theme_id"`
	DarkMode       bool   `json:"dark_mode"`
	IconHash       string `json:"icon_hash"`
}

type InternalPushIcon struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
	Image    []byte `json:"image,omitempty"`
}

type InternalEdgeSync struct {
	Users     []InternalUserAuth `json:"users"`
	IconBlobs map[string][]byte  `json:"icon_blobs"`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
