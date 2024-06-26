package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type Handler struct {
	MaxBodySize int64 // in bytes
	Executor    Executor
	Client      bool
}

type Request struct {
	OperationName string                 `json:"operationName"`
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
	Context       context.Context
}

func set(v interface{}, m interface{}, path string) error {
	var parts []interface{}
	for _, p := range strings.Split(path, ".") {
		if isNumber, err := regexp.MatchString(`\d+`, p); err != nil {
			return err
		} else if isNumber {
			index, _ := strconv.Atoi(p)
			parts = append(parts, index)
		} else {
			parts = append(parts, p)
		}
	}
	for i, p := range parts {
		last := i == len(parts)-1
		switch idx := p.(type) {
		case string:
			if last {
				m.(map[string]interface{})[idx] = v
			} else {
				m = m.(map[string]interface{})[idx]
			}
		case int:
			if last {
				m.([]interface{})[idx] = v
			} else {
				m = m.([]interface{})[idx]
			}
		}
	}
	return nil
}

type File struct {
	File     multipart.File
	Filename string
	Size     int64
}

type Config struct {
	MaxBodySize int64
}

type Executor func(request *Request) interface{}
type Factory func(http.ResponseWriter, *http.Request) interface{}

func New(executor Executor, config *Config) *Handler {
	return &Handler{
		MaxBodySize: config.MaxBodySize,
		Executor:    executor,
	}
}

func errHandler(err error) {
	if errors.Is(err, syscall.EPIPE) {
		return
	} else {
		panic(err)
	}
}

func (self *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	var operations interface{}
	remoteIp := getRemoteIp(r)

	if r.Method == "GET" {
		request := Request{Context: context.WithValue(r.Context(), "header", r.Header)}
		request.Context = context.WithValue(request.Context, "remote-ip", remoteIp)

		// Get query
		if value := r.URL.Query().Get("query"); len(value) == 0 {
			message := fmt.Sprintf("Missing query")
			http.Error(w, message, http.StatusBadRequest)
			return
		} else {
			request.Query = value
		}

		// Get variables
		if value := r.URL.Query().Get("variables"); len(value) == 0 {
			request.Variables = map[string]interface{}{}
		} else if err := json.Unmarshal([]byte(value), &request.Variables); err != nil {
			message := fmt.Sprintf("Bad variables")
			http.Error(w, message, http.StatusBadRequest)
			return
		}

		// Get variables
		if value := r.URL.Query().Get("operationName"); len(value) == 0 {
			request.OperationName = ""
		} else {
			request.OperationName = value
		}
		result := self.Executor(&request)
		if err := json.NewEncoder(w).Encode(result); err != nil {
			message := fmt.Sprintf("Bad operation name")
			http.Error(w, message, http.StatusBadRequest)
			return
		}
	} else if r.Method == "POST" {
		contentType := strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]

		switch contentType {
		case "text/plain", "application/json":
			if r.ContentLength > 0 {
				if err := json.NewDecoder(r.Body).Decode(&operations); err != nil {
					message := fmt.Sprintf("JSON syntax error")
					http.Error(w, message, http.StatusBadRequest)
					return
				}
			}
		case "multipart/form-data":
			// Parse multipart form
			if err := r.ParseMultipartForm(self.MaxBodySize); err != nil {
				errHandler(err)
			}

			// Unmarshal uploads
			var uploads = map[File][]string{}
			var uploadsMap = map[string][]string{}
			if err := json.Unmarshal([]byte(r.Form.Get("map")), &uploadsMap); err != nil {
				message := fmt.Sprintf("JSON syntax error")
				http.Error(w, message, http.StatusBadRequest)
				return
			} else {
				for key, path := range uploadsMap {
					if file, header, err := r.FormFile(key); err != nil {
						message := fmt.Sprintf("JSON syntax error")
						http.Error(w, message, http.StatusBadRequest)
						return
					} else {
						uploads[File{
							File:     file,
							Size:     header.Size,
							Filename: header.Filename,
						}] = path
					}
				}
			}

			// Unmarshal operations
			if err := json.Unmarshal([]byte(r.Form.Get("operations")), &operations); err != nil {
				message := fmt.Sprintf("JSON syntax error")
				http.Error(w, message, http.StatusBadRequest)
				return
			}

			// set uploads to operations
			for file, paths := range uploads {
				for _, path := range paths {
					if err := set(file, operations, path); err != nil {
						errHandler(err)
					}
				}
			}
		}
		switch data := operations.(type) {
		case map[string]interface{}:
			request := Request{}
			if value, ok := data["operationName"]; ok && value != nil {
				if tmp, ok := value.(string); ok {
					request.OperationName = tmp
				}
			}
			if value, ok := data["query"]; ok && value != nil {
				if tmp, ok := value.(string); ok {
					request.Query = tmp
				}
			}
			if value, ok := data["variables"]; ok && value != nil {
				if tmp, ok := value.(map[string]interface{}); ok {
					request.Variables = tmp
				}
			}
			request.Context = context.WithValue(r.Context(), "header", r.Header)
			request.Context = context.WithValue(request.Context, "remote-ip", remoteIp)
			if err := json.NewEncoder(w).Encode(self.Executor(&request)); err != nil {
				message := fmt.Sprintf("JSON syntax error")
				http.Error(w, message, http.StatusBadRequest)
				return
			}
		case []interface{}:
			result := make([]interface{}, len(data))
			for index, operation := range data {
				data := operation.(map[string]interface{})
				request := Request{}
				if value, ok := data["operationName"]; ok {
					if tmp, ok := value.(string); ok {
						request.OperationName = tmp
					}
				}
				if value, ok := data["query"]; ok {
					if tmp, ok := value.(string); ok {
						request.Query = tmp
					}
				}
				if value, ok := data["variables"]; ok {
					if tmp, ok := value.(map[string]interface{}); ok {
						request.Variables = tmp
					}
				}
				request.Context = context.WithValue(r.Context(), "header", r.Header)
				request.Context = context.WithValue(request.Context, "remote-ip", remoteIp)
				result[index] = self.Executor(&request)
			}
			if err := json.NewEncoder(w).Encode(result); err != nil {
				errHandler(err)
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

}

func getRemoteIp(r *http.Request) string {
	if realIp := r.Header.Get("X-Real-IP"); realIp != "" {
		return realIp
	} else if ips := r.Header.Get("X-Forwarded-For"); ips != "" {
		return strings.Split(ips, ",")[0]
	} else if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}
