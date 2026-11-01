package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

type endpoints struct {
	auth, token, api string
}

var (
	googleEndpoints = endpoints{
		auth:  "https://accounts.google.com/o/oauth2/v2/auth",
		token: "https://oauth2.googleapis.com/token",
		api:   "https://openidconnect.googleapis.com/v1/userinfo",
	}
	githubEndpoints = endpoints{
		auth:  "https://github.com/login/oauth/authorize",
		token: "https://github.com/login/oauth/access_token",
		api:   "https://api.github.com",
	}
)

// Google signs users in with Google (scopes openid, email, profile). The identity comes from
// the OpenID Connect userinfo endpoint. Register {PublicURL}{APIPrefix}/auth/google/callback
// as an authorized redirect URI.
func Google(clientID, clientSecret string) Provider {
	return google(clientID, clientSecret, googleEndpoints)
}

func google(clientID, clientSecret string, ep endpoints) Provider {
	return OAuth2(OAuth2Config{
		Name: "google", ClientID: clientID, ClientSecret: clientSecret,
		AuthURL: ep.auth, TokenURL: ep.token,
		Scopes: []string{"openid", "email", "profile"},
		Identity: func(ctx context.Context, c *http.Client, token string) (Identity, error) {
			var u struct {
				Sub           string `json:"sub"`
				Email         string `json:"email"`
				EmailVerified bool   `json:"email_verified"`
				Name          string `json:"name"`
				Picture       string `json:"picture"`
			}
			if err := GetJSON(ctx, c, token, ep.api, &u); err != nil {
				return Identity{}, err
			}
			return Identity{Subject: u.Sub, Email: u.Email, EmailVerified: u.EmailVerified, Name: u.Name, AvatarURL: u.Picture}, nil
		},
	})
}

// GitHub signs users in with GitHub (scopes read:user, user:email). Email is the primary
// verified address from /user/emails; EmailVerified is false when there is none.
func GitHub(clientID, clientSecret string) Provider {
	return github(clientID, clientSecret, githubEndpoints)
}

func github(clientID, clientSecret string, ep endpoints) Provider {
	return OAuth2(OAuth2Config{
		Name: "github", ClientID: clientID, ClientSecret: clientSecret,
		AuthURL: ep.auth, TokenURL: ep.token,
		Scopes: []string{"read:user", "user:email"},
		Identity: func(ctx context.Context, c *http.Client, token string) (Identity, error) {
			var u struct {
				ID        int64  `json:"id"`
				Login     string `json:"login"`
				Name      string `json:"name"`
				AvatarURL string `json:"avatar_url"`
			}
			if err := GetJSON(ctx, c, token, ep.api+"/user", &u); err != nil {
				return Identity{}, err
			}
			if u.ID == 0 {
				return Identity{}, errors.New("github user without id")
			}
			var emails []struct {
				Email    string `json:"email"`
				Primary  bool   `json:"primary"`
				Verified bool   `json:"verified"`
			}
			if err := GetJSON(ctx, c, token, ep.api+"/user/emails", &emails); err != nil {
				return Identity{}, err
			}
			id := Identity{Subject: strconv.FormatInt(u.ID, 10), Name: u.Name, AvatarURL: u.AvatarURL}
			if id.Name == "" {
				id.Name = u.Login
			}
			for _, e := range emails {
				if e.Primary && e.Verified {
					id.Email, id.EmailVerified = e.Email, true
				}
			}
			return id, nil
		},
	})
}
