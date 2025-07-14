package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {

	const maxUploadSize = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	videoIDStr := r.PathValue("videoID")

	videoID, err := uuid.Parse(videoIDStr)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video id", err)
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

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}

	if video.CreateVideoParams.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You do not have rights to this video", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType := header.Header.Get("Content-Type")

	mediaType, _, err = mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content Type", err)
	}

	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Usupported media type", err)
		return
	}

	outFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create file on disk", err)
		return
	}
	defer os.Remove(outFile.Name())
	defer outFile.Close()

	_, err = io.Copy(outFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't save file on disk", err)
		return
	}

	_, err = outFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't seek to beginning of video file", err)
		return
	}

	randomBytes := make([]byte, 32)
	n, err := rand.Read(randomBytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to generate video file name", err)
		return
	}
	if n != 32 {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate video file name", err)
		return
	}

	filenameHex := hex.EncodeToString(randomBytes)
	ext := filepath.Ext(header.Filename)
	key := filenameHex + ext

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &key,
		Body:        outFile,
		ContentType: &mediaType,
		//ACL:         types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload video to S3", err)
		return
	}

	VideolURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)
	video.VideoURL = &VideolURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update the URL of video record", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
