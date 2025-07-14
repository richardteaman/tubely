package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// TODO: implement the upload here
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	if video.CreateVideoParams.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You do not have rights to this video", nil)
		return
	}

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType := header.Header.Get("Content-Type")

	mediaType, _, err = mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content Type", err)
		return
	}

	if mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/jpg" {
		respondWithError(w, http.StatusBadRequest, "Unsupported media type", err)
		return
	}

	ext := getExtentionFromMediaType(mediaType)
	if ext == "" {
		respondWithError(w, http.StatusBadRequest, "Unsupported media type", err)
		return
	}
	//fmt.Println("ext: ", ext)

	//
	randomBytes := make([]byte, 32)
	n, err := rand.Read(randomBytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to generate name for thumbnail", err)
		return
	}
	if n != 32 {
		respondWithError(w, http.StatusInternalServerError, "Error while generating thumnial name (short)", err)
	}

	filename := base64.RawURLEncoding.EncodeToString(randomBytes) + ext
	path := filepath.Join(cfg.assetsRoot, filename)

	outFile, err := os.Create(path)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create thumbnail file", err)
		return
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save thumbnail file", err)
	}

	ThumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, filename)
	video.ThumbnailURL = &ThumbnailURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update the thumbnail of the video record", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}

func getExtentionFromMediaType(mediaType string) string {
	switch strings.ToLower(mediaType) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	default:
		return ""
	}
}
