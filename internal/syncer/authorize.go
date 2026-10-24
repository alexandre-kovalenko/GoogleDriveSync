package syncer

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const authorizationTimeout = 5 * time.Minute

type authorizationResult struct {
	code string
	err  error
}

// Authorize runs the one-time interactive authorization flow and stores the
// resulting refresh token in configPath. Subsequent sync runs are noninteractive.
func Authorize(ctx context.Context, credentials OAuthConfig, configPath string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start OAuth callback listener: %w", err)
	}
	defer listener.Close()

	redirectURL := "http://" + listener.Addr().String()
	oauthConfig := &oauth2.Config{
		ClientID:     credentials.ClientID,
		ClientSecret: credentials.ClientSecret,
		Endpoint:     google.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       []string{"https://www.googleapis.com/auth/drive"},
	}
	state := oauth2.GenerateVerifier()
	pkceVerifier := oauth2.GenerateVerifier()
	authURL := oauthConfig.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(pkceVerifier),
	)

	results := make(chan authorizationResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "OAuth state mismatch. Return to the application and retry.", http.StatusBadRequest)
			return
		}
		query := r.URL.Query()
		if providerErr := query.Get("error"); providerErr != "" {
			description := query.Get("error_description")
			resultErr := fmt.Errorf("Google authorization failed: %s", providerErr)
			if description != "" {
				resultErr = fmt.Errorf("%w (%s)", resultErr, description)
			}
			http.Error(w, "Authorization failed. Return to the terminal for details.", http.StatusBadRequest)
			results <- authorizationResult{err: resultErr}
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "Google did not return an authorization code.", http.StatusBadRequest)
			results <- authorizationResult{err: errors.New("Google callback did not include an authorization code")}
			return
		}
		_, _ = fmt.Fprintln(w, "Authorization received. You can return to the terminal.")
		results <- authorizationResult{code: code}
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	fmt.Println("Opening Google authorization. If it does not open, visit this URL:")
	fmt.Println(authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("Could not open a browser automatically: %v\n", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, authorizationTimeout)
	defer cancel()
	var result authorizationResult
	select {
	case result = <-results:
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("OAuth callback server: %w", err)
		}
		return errors.New("OAuth callback server stopped before authorization completed")
	case <-waitCtx.Done():
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("authorization timed out after %s", authorizationTimeout)
		}
		return fmt.Errorf("authorization canceled: %w", waitCtx.Err())
	}
	_ = server.Close()
	if result.err != nil {
		return result.err
	}

	token, err := oauthConfig.Exchange(waitCtx, result.code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return fmt.Errorf("exchange authorization code: %w", err)
	}
	if token.RefreshToken == "" {
		return errors.New("Google did not return a refresh token; revoke this app's access in your Google Account and run -authorize again")
	}
	if err := SaveRefreshToken(configPath, token.RefreshToken); err != nil {
		return err
	}
	return nil
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	cmd := exec.Command(command, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
