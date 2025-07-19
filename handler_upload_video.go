package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
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

	aspectRatio, err := getVideoAspectRatio(outFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't determine video's aspect ratio", err)
		return
	}

	var aspectPrefix string
	switch aspectRatio {
	case "16:9":
		aspectPrefix = "landscape/"
	case "9:16":
		aspectPrefix = "portrait/"
	default:
		aspectPrefix = "other/"
	}

	processedFilePath, err := processVideoForFastStart(outFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't generate processed video path", err)
		return
	}

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't open processed file", err)
		return
	}
	defer os.Remove(processedFilePath)
	defer processedFile.Close()

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
	key := aspectPrefix + filenameHex + ext

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &key,
		Body:        processedFile,
		ContentType: &mediaType,
		//ACL:         types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload video to S3", err)
		return
	}

	//VideolURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)
	//VideolURL := fmt.Sprintf("%s,%s", cfg.s3Bucket, key) // for use with presigned URL's
	VideolURL := fmt.Sprintf("https://%s/%s", cfg.s3CfDistribution, key) // for use with presigned URL's

	video.VideoURL = &VideolURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update the URL of video record", err)
		return
	}

	/*
		signedVideo, err := cfg.dbVideoToSignedVideo(video)
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "Failed to retrive signed video", err)
			return
		}
	*/

	respondWithJSON(w, http.StatusOK, video)
}

func getVideoAspectRatio(filePath string) (string, error) {

	//commandStr := fmt.Sprintf("ffprobe -v error -print_format json -show_streams %s",filePath)
	//cmd := exec.Command(commandStr)
	cmd := exec.Command("ffprobe",
		"-v", "error", "-print_format",
		"json", "-show_streams",
		filePath,
	)

	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	if err != nil {
		return "", err
	}

	type stream struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}

	type ffprobeOutput struct {
		Streams []stream `json:"streams"`
	}

	var result ffprobeOutput
	err = json.Unmarshal(out.Bytes(), &result)
	if err != nil {
		return "", err
	}

	if len(result.Streams) < 1 {
		return "", fmt.Errorf("empty video streams")
	}

	width := result.Streams[0].Width
	height := result.Streams[0].Height

	aspectRatio := float64(width) / float64(height)
	const toleracne = 0.05

	switch {
	case math.Abs(aspectRatio-1.777) < toleracne:
		return "16:9", nil
	case math.Abs(aspectRatio-0.5625) < toleracne:
		return "9:16", nil
	default:
		return "other", nil
	}
}

func processVideoForFastStart(filePath string) (string, error) {
	out := filePath + ".processing"

	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", out)
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	return out, nil
}

/*
func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	req, err := presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}

	return req.URL, nil
}
*/
