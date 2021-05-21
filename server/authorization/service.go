package authorization

import (
	"errors"
	"github.com/jitsucom/jitsu/server/logging"
	"github.com/jitsucom/jitsu/server/resources"
	"github.com/jitsucom/jitsu/server/uuid"
	"github.com/spf13/viper"
	"strings"
	"sync"
	"time"
)

const (
	serviceName            = "authorization"
	viperApiKeysKey        = "api_keys"
	deprecatedViperAuthKey = "server.auth"
	deprecatedViperS2SKey  = "server.s2s_auth"

	defaultTokenID = "defaultid"
)

type Service struct {
	sync.RWMutex

	tokensHolder *TokensHolder
	//will call after every reloading
	DestinationsForceReload func()
}

func NewService() (*Service, error) {
	service := &Service{}

	reloadSec := viper.GetInt("server.api_keys_reload_sec")
	if reloadSec == 0 {
		//backward compatibility
		reloadSec = viper.GetInt("server.auth_reload_sec")
	}

	if reloadSec == 0 {
		return nil, errors.New("server.api_keys_reload_sec can't be empty")
	}

	//if api_keys is used => strict tokens
	if viper.IsSet(viperApiKeysKey) {
		viper.SetDefault("server.strict_auth_tokens", true)
	}
	viperKey := viperApiKeysKey
	if !viper.IsSet(viperKey) && viper.IsSet(deprecatedViperAuthKey) {
		viperKey = deprecatedViperAuthKey
	}

	//deprecated viper s2s key
	deprecatedS2SAuth := viper.GetStringSlice(deprecatedViperS2SKey)

	var tokens []Token
	err := viper.UnmarshalKey(viperKey, &tokens)
	if err == nil {
		for _, s2sauth := range deprecatedS2SAuth {
			tokens = append(tokens, Token{ServerSecret: s2sauth})
		}
		service.tokensHolder = reformat(tokens)
	} else {
		auth := viper.GetStringSlice(viperKey)

		if len(auth) == 1 {
			authSource := auth[0]
			if strings.HasPrefix(authSource, "http://") || strings.HasPrefix(authSource, "https://") {
				resources.Watch(serviceName, authSource, resources.LoadFromHTTP, service.updateTokens, time.Duration(reloadSec)*time.Second)
			} else if strings.HasPrefix(authSource, "file://") || strings.HasPrefix(authSource, "/") {
				resources.Watch(serviceName, strings.Replace(authSource, "file://", "", 1), resources.LoadFromFile, service.updateTokens, time.Duration(reloadSec)*time.Second)
			} else if strings.HasPrefix(authSource, "{") && strings.HasSuffix(authSource, "}") {
				tokensHolder, err := parseFromBytes([]byte(authSource))
				if err != nil {
					return nil, err
				}
				service.tokensHolder = tokensHolder
			} else {
				//plain token
				service.tokensHolder = fromStrings(auth, deprecatedS2SAuth)
			}
		} else {
			//array of tokens
			service.tokensHolder = fromStrings(auth, deprecatedS2SAuth)
		}

	}

	if service.tokensHolder.IsEmpty() {
		//autogenerated
		generatedTokenSecret := uuid.New()
		generatedToken := Token{
			ID:           defaultTokenID,
			ClientSecret: generatedTokenSecret,
			ServerSecret: generatedTokenSecret,
			Origins:      []string{},
		}

		service.tokensHolder = reformat([]Token{generatedToken})
		logging.Info("Empty authorization 'api_keys' config. Auto generate API Key:", generatedTokenSecret)
	}

	return service, nil
}

//GetClientOrigins return origins by client_secret
func (s *Service) GetClientOrigins(clientSecret string) ([]string, bool) {
	s.RLock()
	defer s.RUnlock()

	origins, ok := s.tokensHolder.clientTokensOrigins[clientSecret]
	return origins, ok
}

//GetServerOrigins return origins by server_secret
func (s *Service) GetServerOrigins(serverSecret string) ([]string, bool) {
	s.RLock()
	defer s.RUnlock()

	origins, ok := s.tokensHolder.serverTokensOrigins[serverSecret]
	return origins, ok
}

//GetAllTokenIDs return all token ids
func (s *Service) GetAllTokenIDs() []string {
	s.RLock()
	defer s.RUnlock()

	return s.tokensHolder.ids
}

//GetAllIDsByToken return token ids by token identity(client_secret/server_secret/token id)
func (s *Service) GetAllIDsByToken(tokenIDentity []string) (ids []string) {
	s.RLock()
	defer s.RUnlock()

	deduplication := map[string]bool{}
	for _, tokenFilter := range tokenIDentity {
		tokenObj, ok := s.tokensHolder.all[tokenFilter]
		if ok {
			deduplication[tokenObj.ID] = true
		}
	}

	for id := range deduplication {
		ids = append(ids, id)
	}
	return
}

//GetTokenID return token id by client_secret/server_secret/token id
//return "" if token wasn't found
func (s *Service) GetTokenID(tokenFilter string) string {
	s.RLock()
	defer s.RUnlock()

	token, ok := s.tokensHolder.all[tokenFilter]
	if ok {
		return token.ID
	}
	return ""
}

//parse and set tokensHolder with lock
func (s *Service) updateTokens(payload []byte) {
	tokenHolder, err := parseFromBytes(payload)
	if err != nil {
		logging.Errorf("Error updating authorization tokens: %v", err)
	} else {
		s.Lock()
		s.tokensHolder = tokenHolder
		s.Unlock()

		//we should reload destinations after all changes in authorization service
		if s.DestinationsForceReload != nil {
			s.DestinationsForceReload()
		}
	}
}
