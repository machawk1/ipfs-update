package util

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	api "github.com/ipfs/go-ipfs-api"
	config "github.com/ipfs/ipfs-update/config"
	stump "github.com/whyrusleeping/stump"
)

var (
	GlobalGatewayUrl = "https://ipfs.io"
	LocalApiUrl      = "http://localhost:5001"
	IpfsVersionPath  = "/ipns/dist.ipfs.io"
)

func init() {
	if dist := os.Getenv("IPFS_DIST_PATH"); dist != "" {
		IpfsVersionPath = dist
	}
}

const fetchSizeLimit = 1024 * 1024 * 512

func ApiEndpoint(ipfspath string) (string, error) {
	apifile := filepath.Join(ipfspath, "api")

	val, err := ioutil.ReadFile(apifile)
	if err != nil {
		return "", err
	}

	parts := strings.Split(string(val), "/")
	if len(parts) != 5 {
		return "", fmt.Errorf("incorrectly formatted api string: %q", string(val))
	}

	return parts[2] + ":" + parts[4], nil
}

func httpGet(url string) (*http.Response, error) {
    // Do HTTP HEAD for payload size, retain connection
    headResponse, err := http.Head(url)

    out, err := ioutil.TempFile(os.TempDir(), "ipfs")
    defer os.Remove(out.Name())

    if err != nil {
    	return nil, fmt.Errorf("http.Head error: %s", err)
    }

    defer out.Close()

    size, err := strconv.Atoi(headResponse.Header.Get("Content-Length"))
    
    if err != nil {
    	return nil, fmt.Errorf("http.Head Content-Length error: %s", err)
    }

    headResponse.Body.Close()

    done := make(chan int64)

    go PrintProgress(done, out.Name(), int64(size))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("http.NewRequest error: %s", err)
	}

	req.Header.Set("User-Agent", config.GetUserAgent())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http.DefaultClient.Do error: %s", err)
	}

    // Following line causes ipfs binary download to fail due to EOF
    n, err := io.Copy(out, resp.Body)

	if err != nil {
		fmt.Printf("ERRORRRR")
		return nil, fmt.Errorf("Error writing temp file to disk: %s", err)
	}

	done <- n

	return resp, nil
}

func PrintProgress(done chan int64, path string, total int64) {
	var halt bool = false
	var progressString string = "Download progress:"
	for {
		select {
		case <- done:
			halt = true
		default:
			file, err := os.Open(path)
			if err != nil {
				return
			}

			fi, err := file.Stat()
			if err != nil {
				return
			}

			size := fi.Size()

			if size == 0 {
				size = 1
			}

			var percent float64 = float64(size) / float64(total) * 100

			fmt.Printf("\r%s %.0f%%", progressString, percent)
		}

		if halt {
			fmt.Printf("\r%s COMPLETE\n", progressString)
			break
		}

		time.Sleep(time.Second)
	}
}

func httpFetch(url string) (io.ReadCloser, error) {
	stump.VLog("fetching url: %s", url)
	resp, err := httpGet(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		stump.Error("fetching resource: %s", resp.Status)
		mes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("error reading error body: %s", err)
		}

		return nil, fmt.Errorf("%s: %s", resp.Status, string(mes))
	}

	return newLimitReadCloser(resp.Body, fetchSizeLimit), nil
}

func Fetch(ipfspath string) (io.ReadCloser, error) {
	stump.VLog("  - fetching %q", ipfspath)
	ep, err := ApiEndpoint(IpfsDir())
	if err == nil {
		sh := api.NewShell(ep)
		if sh.IsUp() {
			stump.VLog("  - using local ipfs daemon for transfer")
			rc, err := sh.Cat(ipfspath)
			if err != nil {
				return nil, err
			}

			return newLimitReadCloser(rc, fetchSizeLimit), nil
		}
	}

	return httpFetch(GlobalGatewayUrl + ipfspath)
}

type limitReadCloser struct {
	io.Reader
	io.Closer
}

func newLimitReadCloser(rc io.ReadCloser, limit int64) io.ReadCloser {
	return limitReadCloser{
		Reader: io.LimitReader(rc, limit),
		Closer: rc,
	}
}

// This function is needed because os.Rename doesnt work across filesystem
// boundaries.
func CopyTo(src, dest string) error {
	fi, err := os.Open(src)
	if err != nil {
		return err
	}
	defer fi.Close()

	trgt, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer trgt.Close()

	_, err = io.Copy(trgt, fi)
	return err
}

func Move(src, dest string) error {
	err := CopyTo(src, dest)
	if err != nil {
		return err
	}

	return os.Remove(src)
}

func IpfsDir() string {
	def := filepath.Join(os.Getenv("HOME"), ".ipfs")

	ipfs_path := os.Getenv("IPFS_PATH")
	if ipfs_path != "" {
		def = ipfs_path
	}

	return def
}

func HasDaemonRunning() bool {
	shell := api.NewShell(LocalApiUrl)
	shell.SetTimeout(1 * time.Second)
	return shell.IsUp()
}

func RunCmd(p, bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "IPFS_PATH="+p)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, string(out))
	}

	if out[len(out)-1] == '\n' {
		return string(out[:len(out)-1]), nil
	}
	return string(out), nil
}

func BeforeVersion(check, cur string) bool {
	aparts := strings.Split(check[1:], ".")
	bparts := strings.Split(cur[1:], ".")
	for i := 0; i < 3; i++ {
		an, err := strconv.Atoi(aparts[i])
		if err != nil {
			return false
		}
		bn, err := strconv.Atoi(bparts[i])
		if err != nil {
			return false
		}
		if bn < an {
			return true
		}
		if bn > an {
			return false
		}
	}
	return false
}

func BoldText(s string) string {
	return fmt.Sprintf("\033[1m%s\033[0m")
}
