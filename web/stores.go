package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/aristath/songya"
)

type storeNavigationKey struct{}
type storeNavigation struct {
	Selected string
	Stores   []string
	Mobile   bool
}

type StoreApplication struct {
	http.Handler
	applications map[string]*Application
	mobile       *mobileServer
}

func (a *StoreApplication) Close() error {
	var issues []error
	for _, app := range a.applications {
		issues = append(issues, app.Close())
	}
	return errors.Join(issues...)
}

func HandlerForStores(config songya.ApplicationConfiguration) *StoreApplication {
	application := &StoreApplication{applications: map[string]*Application{}, mobile: newMobileServer(config)}
	issues := map[string]error{}
	names := []string{}
	for name, directory := range config.Stores {
		names = append(names, name)
		store, err := songya.Open(directory)
		if err != nil {
			issues[name] = err
			continue
		}
		application.applications[name] = Handler(store)
	}
	slices.Sort(names)
	password := sha256.Sum256([]byte(config.Password))
	application.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		if application.mobile.public(w, r) {
			return
		}
		_, supplied, ok := r.BasicAuth()
		candidate := sha256.Sum256([]byte(supplied))
		admin := ok && config.Password != "" && subtle.ConstantTimeCompare(password[:], candidate[:]) == 1
		access := mobileAccess{}
		if !admin {
			var err error
			access, err = application.mobile.authenticate(r)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Basic realm="Songya", charset="UTF-8"`)
				http.Error(w, "Administrator authentication or a paired device is required.", http.StatusUnauthorized)
				return
			}
		}
		visible := names
		if !admin {
			visible = []string{access.Store}
		}
		if r.URL.Path == "/api/mobile" || strings.HasPrefix(r.URL.Path, "/api/mobile/") || r.URL.Path == "/api/logout" {
			application.mobile.serve(w, r, access, admin)
			return
		}
		if r.URL.Path == "/android.apk" {
			if config.AndroidAPK == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.android.package-archive")
			w.Header().Set("Content-Disposition", `attachment; filename="Songya.apk"`)
			http.ServeFile(w, r, config.AndroidAPK)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/stores/"+visible[0]+"/", http.StatusSeeOther)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			for _, app := range application.applications {
				app.ServeHTTP(w, r)
				return
			}
			http.Error(w, "No readable stores.", http.StatusServiceUnavailable)
			return
		}
		path, found := strings.CutPrefix(r.URL.Path, "/stores/")
		name, suffix, _ := strings.Cut(path, "/")
		if !admin && name != access.Store {
			http.NotFound(w, r)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		if issue := issues[name]; issue != nil {
			http.Error(w, issue.Error(), http.StatusServiceUnavailable)
			return
		}
		app := application.applications[name]
		if app == nil {
			http.NotFound(w, r)
			return
		}
		// Keep browser operations under the Basic-auth directory that was opened.
		if suffix == "api/mobile" || strings.HasPrefix(suffix, "api/mobile/") || suffix == "api/logout" || suffix == "android.apk" {
			request := r.Clone(r.Context())
			request.URL.Path = "/" + suffix
			request.URL.RawPath = ""
			if suffix == "android.apk" {
				if config.AndroidAPK == "" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/vnd.android.package-archive")
				w.Header().Set("Content-Disposition", `attachment; filename="Songya.apk"`)
				http.ServeFile(w, request, config.AndroidAPK)
				return
			}
			application.mobile.serve(w, request, access, admin)
			return
		}
		if !strings.Contains(path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusSeeOther)
			return
		}
		request := r.Clone(context.WithValue(r.Context(), storeNavigationKey{}, storeNavigation{Selected: name, Stores: visible, Mobile: access.Device != ""}))
		request.URL.Path = "/" + suffix
		request.URL.RawPath = ""
		app.ServeHTTP(w, request)
	})
	return application
}
