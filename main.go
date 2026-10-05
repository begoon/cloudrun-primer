package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"cloud.google.com/go/compute/metadata"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/dustin/go-humanize"
	_ "golang.org/x/crypto/x509roots/fallback"
)

type Writer struct {
	writer    http.ResponseWriter
	started   time.Time
	size      uint64
	block     uint64
	outputErr error
}

const blockSize = 1024 * 1024 * 50

func (w *Writer) Write(p []byte) (n int, err error) {
	n = len(p)
	w.size += uint64(n)
	w.block += uint64(n)
	if w.block >= blockSize {
		elapsed := time.Since(w.started)
		throughput := float64(w.size) / elapsed.Seconds()
		_, err = fmt.Fprintf(
			w.writer, "block %s/%s | throughput %s/s | elapsed %s\n",
			humanize.Bytes(w.block), humanize.Bytes(w.size),
			humanize.Bytes(uint64(throughput)),
			elapsed,
		)
		if err != nil {
			w.outputErr = err
			return n, err
		}
		w.block = w.block - blockSize
		err = flushResponse(w.writer)
		w.outputErr = err
	}
	return
}

func flushResponse(w http.ResponseWriter) error {
	err := http.NewResponseController(w).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

const large = "https://fsn1-speed.hetzner.com/100MB.bin"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		env := os.Environ()
		for _, e := range env {
			w.Write([]byte(e + "\n"))
		}
		ctx := r.Context()

		ok := func(v string, err error) string {
			if err != nil {
				return err.Error()
			}
			return v
		}

		cpu := strconv.Itoa(runtime.NumCPU())
		w.Write([]byte("CPU=" + cpu + "\n"))

		gce := metadata.OnGCE()
		w.Write([]byte("GCE=" + strconv.FormatBool(gce) + "\n"))

		if gce {
			w.Write([]byte("\n"))
			w.Write([]byte("project=" + ok(metadata.ProjectIDWithContext(ctx)) + "\n"))
			w.Write([]byte("project_id=" + ok(metadata.NumericProjectIDWithContext(ctx)) + "\n"))
			w.Write([]byte("zone=" + ok(metadata.ZoneWithContext(ctx)) + "\n"))
			w.Write([]byte("email=" + ok(metadata.EmailWithContext(ctx, "")) + "\n"))
		}

		iapUser := r.Header.Get("X-Goog-Authenticated-User-Email")
		if iapUser != "" {
			w.Write([]byte("\n"))
			w.Write([]byte("X-Goog-Authenticated-User-Email=" + iapUser + "\n"))

			iapJWT := r.Header.Get("X-Goog-IAP-JWT-Assertion")
			if iapJWT != "" {
				token, err := iapValidateJWT(ctx, iapJWT)
				if err != nil {
					w.Write([]byte("X-Goog-IAP-JWT-Assertion error: " + err.Error() + "\n"))
				} else {
					w.Write([]byte("X-Goog-IAP-JWT-Assertion claims:\n"))
					claims, err := json.MarshalIndent(token.Claims, "", "  ")
					if err != nil {
						w.Write([]byte("marshal claims: " + err.Error() + "\n"))
					} else {
						w.Write(claims)
						w.Write([]byte("\n"))
					}
				}
			}
		}
	})

	http.HandleFunc("/speed", speedHandler(http.DefaultClient, large, 2*time.Minute))

	fs := http.FileServer(http.Dir("/"))
	http.Handle("/fs/", http.StripPrefix("/fs/", fs))

	http.HandleFunc("/ip", ipHandler(http.DefaultClient, "https://api.ipify.org?format=json"))

	http.HandleFunc("GET /ls/{path...}", ls("/ls"))

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

func speedHandler(client *http.Client, url string, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Add("User-Agent", "curl/8.7.1")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("speed download failed: %v", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Println("response status:", resp.Status)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "speed download returned "+resp.Status, http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		started := time.Now()
		ww := &Writer{started: started, writer: w}
		if _, err := fmt.Fprintf(w, "url=%s\nstarted at %s\n", url, started.Format(time.RFC3339Nano)); err != nil {
			log.Printf("speed output failed: %v", err)
			return
		}
		if err := flushResponse(w); err != nil {
			log.Printf("speed flush failed: %v", err)
			return
		}
		written, err := io.Copy(ww, resp.Body)
		if err != nil {
			log.Printf("speed download failed: %v", err)
			if ww.outputErr == nil {
				if _, writeErr := fmt.Fprintf(w, "error=download failed: %v\n", err); writeErr != nil {
					log.Printf("speed output failed: %v", writeErr)
				}
			}
			return
		}
		elapsed := time.Since(started)
		throughput := float64(ww.size) / elapsed.Seconds()
		_, err = fmt.Fprintf(
			w, "downloaded %s | throughput %s/s | elapsed %s\n",
			humanize.Bytes(uint64(written)),
			humanize.Bytes(uint64(throughput)),
			elapsed,
		)
		if err != nil {
			log.Printf("speed output failed: %v", err)
		}
	}
}

func ipHandler(client *http.Client, url string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("IP lookup failed: %v", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		log.Println("response status:", resp.Status)
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "IP lookup returned "+resp.Status, http.StatusBadGateway)
			return
		}

		v := struct {
			IP string `json:"ip"`
		}{}
		err = json.NewDecoder(resp.Body).Decode(&v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Write([]byte(v.IP))
	}
}

func ls(prefix string) http.HandlerFunc {
	log.Println("prefix", prefix)
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, prefix)
		log.Println("URL", r.URL.Path, "->", "path", path)

		fi, err := os.Stat(path)
		if err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !fi.IsDir() {
			http.ServeFile(w, r, path)
			return
		}

		files, err := os.ReadDir(path)
		if err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		type FileInfo struct {
			Name    string
			Size    int64
			ModTime string
			IsDir   bool
		}

		var fileList []FileInfo

		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				continue
			}
			fileList = append(fileList, FileInfo{
				Name:    info.Name(),
				Size:    info.Size(),
				ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
				IsDir:   info.IsDir(),
			})
		}

		tmpl, err := template.New("filelist").Parse(`
        <table>
            <thead>
                <tr><th>name</th><th>size</th><th>modified</th></tr>
            </thead>
            <tbody>
                {{range .}}
                <tr>
                    <td><a href="` + prefix + path + `/{{.Name}}">{{.Name}}</a></td>
                    <td>
						{{if not .IsDir}}
						{{.Size}} bytes
						{{end}}
					</td>
                    <td>{{.ModTime}}</td>
                </tr>
                {{end}}
            </tbody>
        </table>`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = tmpl.Execute(w, fileList)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

const iapPublicKeysURL = "https://www.gstatic.com/iap/verify/public_key-jwk"

func iapValidateJWT(ctx context.Context, jwtToken string) (*jwt.Token, error) {
	keyfunc, err := keyfunc.NewDefaultCtx(ctx, []string{iapPublicKeysURL})
	if err != nil {
		return nil, fmt.Errorf("fetch IAP keys from %s: %w", iapPublicKeysURL, err)
	}

	token, err := jwt.Parse(jwtToken, keyfunc.Keyfunc)
	if err != nil {
		return nil, fmt.Errorf("parse/verify JWT [%s]: %w", jwtToken, err)
	}

	log.Println("token", token)

	if !token.Valid {
		return nil, errors.New("invalid JWT: " + token.Raw)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("invalid JWT claims type")
	}

	log.Println("claims", claims)
	return token, nil
}
