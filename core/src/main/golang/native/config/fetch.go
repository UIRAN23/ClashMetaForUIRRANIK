package config

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/metacubex/mihomo/log"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	U "net/url"
	"os"
	P "path"
	"runtime"
	"strings"
	"time"

	"cfa/native/app"

	clashHttp "github.com/metacubex/mihomo/component/http"
)

type Status struct {
	Action      string   `json:"action"`
	Args        []string `json:"args"`
	Progress    int      `json:"progress"`
	MaxProgress int      `json:"max"`
}

func openUrl(ctx context.Context, url string) (io.ReadCloser, error) {
	response, err := clashHttp.HttpRequest(ctx, url, http.MethodGet, http.Header{"User-Agent": {"ClashMetaForAndroid/" + app.VersionName()}}, nil)

	if err != nil {
		return nil, err
	}

	return response.Body, nil
}

func openUrlAsString(ctx context.Context, url string) (string, error) {
	body, requestErr := openUrl(ctx, url)

	if requestErr != nil {
		return "", requestErr
	}

	data, err := io.ReadAll(body)
	defer body.Close()
	if err != nil {
		return "", err
	}
	content := string(data)
	return content, nil
}

func openUrlAsYaml(ctx context.Context, url string) (map[string]interface{}, error) {
	content, _ := openUrlAsString(ctx, url)
	var config map[string]interface{}
	err := yaml.Unmarshal([]byte(content), &config)
	if err != nil {
		return nil, err
	}
	return config, nil
}

func openContent(url string) (io.ReadCloser, error) {
	return app.OpenContent(url)
}

func fetch(url *U.URL, file string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var reader io.ReadCloser
	var err error

	switch url.Scheme {
	case "http", "https":
		reader, err = openUrl(ctx, url.String())
	case "content":
		reader, err = openContent(url.String())
	default:
		err = fmt.Errorf("unsupported scheme %s of %s", url.Scheme, url)
	}

	if err != nil {
		return err
	}

	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	content := string(data)
	parsedContent := applyParsers(ctx, content, url)
	log.Debugln("最终subscribe:%s", parsedContent)

	_ = os.MkdirAll(P.Dir(file), 0700)

	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0600)
	if err != nil {
		return err
	}

	defer f.Close()

	_, err = f.WriteString(parsedContent)
	if err != nil {
		_ = os.Remove(file)
	}

	return err
}

func applyParsers(ctx context.Context, subscribeOriginalStr string, subscribeUrl *U.URL) string {
	if !subscribeUrl.Query().Has("parsers") {
		log.Debugln("需要处理parsers")
		return subscribeOriginalStr
	}

	var subscribe map[string]interface{}

	err := yaml.Unmarshal([]byte(subscribeOriginalStr), &subscribe)
	if err != nil {
		log.Debugln("failed to parse YAML: %v", err)
		return fmt.Sprintf("failed to parse YAML: %v", err)
	}

	var parsersUrl = subscribeUrl.Query().Get("parsers")
	log.Debugln("找到parsersURL: %s", parsersUrl)
	parsersContainerYml, parsersErr := openUrlAsYaml(ctx, parsersUrl)
	if parsersErr != nil {
		log.Debugln("拉取parsers失败: %v", parsersErr)
		return subscribeOriginalStr
	}

	parsersContainer, parsersContainerExist := parsersContainerYml["parsers"].(map[string]interface{})
	if !parsersContainerExist {
		log.Debugln("parsers容器中不存在parsers节点")
		return subscribeOriginalStr
	}

	parsers, parsersExist := parsersContainer["yaml"].(map[string]interface{})
	if !parsersExist {
		log.Debugln("parsers容器中不存在yaml节点")
		return subscribeOriginalStr
	}

	subscribe = prependArr(subscribe, "proxies", parsers, "prepend-proxies")
	subscribe = prependArr(subscribe, "proxy-groups", parsers, "prepend-proxy-groups")
	subscribe = prependArr(subscribe, "rules", parsers, "prepend-rules")

	// patch-proxy-groups: изменяет существующие группы по имени
	// Поддерживает: type, exclude-filter, filter, interval, tolerance, lazy
	// Пример в my_rules.yaml:
	//   patch-proxy-groups:
	//     - name: "🚀 Best Ping"
	//       exclude-filter: "🇷🇺"
	//     - name: "⚡ Antiblock Auto"
	//       type: select
	subscribe = patchProxyGroups(subscribe, parsers)

	yamlBytes, err := yaml.Marshal(subscribe)
	if err != nil {
		log.Debugln("failed to marshal YAML: %v", err)
		return fmt.Sprintf("failed to marshal YAML: %v", err)
	}

	return string(yamlBytes)
}

// patchProxyGroups applies patch-proxy-groups from parsers to existing groups in subscribe.
// It finds each group by name and merges the patch fields into it.
// Supported fields: type, exclude-filter, filter, interval, tolerance, lazy, url
func patchProxyGroups(subscribe map[string]interface{}, parsers map[string]interface{}) map[string]interface{} {
	patches, ok := parsers["patch-proxy-groups"].([]interface{})
	if !ok {
		log.Debugln("patch-proxy-groups not found in parsers, skipping")
		return subscribe
	}

	groups, ok := subscribe["proxy-groups"].([]interface{})
	if !ok {
		log.Debugln("proxy-groups not found in subscribe, skipping patch")
		return subscribe
	}

	for _, patchRaw := range patches {
		patch, ok := patchRaw.(map[string]interface{})
		if !ok {
			continue
		}

		targetName, ok := patch["name"].(string)
		if !ok {
			continue
		}

		log.Debugln("patch-proxy-groups: applying patch to group %s", targetName)

		for i, groupRaw := range groups {
			group, ok := groupRaw.(map[string]interface{})
			if !ok {
				continue
			}

			groupName, ok := group["name"].(string)
			if !ok {
				continue
			}

			if groupName != targetName {
				continue
			}

			// Apply all fields from patch except "name"
			for key, value := range patch {
				if key == "name" {
					continue
				}

				// Special handling: exclude-filter filters the proxies list
				if key == "exclude-filter" {
					excludeFilter, ok := value.(string)
					if ok && excludeFilter != "" {
						group = applyExcludeFilter(group, excludeFilter)
						log.Debugln("patch-proxy-groups: applied exclude-filter '%s' to group %s", excludeFilter, targetName)
					}
					// Also store the exclude-filter field itself
					group[key] = value
				} else {
					group[key] = value
					log.Debugln("patch-proxy-groups: set %s=%v on group %s", key, value, targetName)
				}
			}

			groups[i] = group
			break
		}
	}

	subscribe["proxy-groups"] = groups
	return subscribe
}

// applyExcludeFilter removes proxies from a group whose names start with any of the
// pipe-separated flag/prefix patterns in excludeFilter.
// Example excludeFilter: "🇷🇺" or "🇷🇺|🇰🇿"
func applyExcludeFilter(group map[string]interface{}, excludeFilter string) map[string]interface{} {
	proxies, ok := group["proxies"].([]interface{})
	if !ok {
		return group
	}

	patterns := strings.Split(excludeFilter, "|")

	filtered := make([]interface{}, 0, len(proxies))
	for _, proxyRaw := range proxies {
		proxyName, ok := proxyRaw.(string)
		if !ok {
			filtered = append(filtered, proxyRaw)
			continue
		}

		excluded := false
		for _, pattern := range patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern != "" && strings.HasPrefix(proxyName, pattern) {
				excluded = true
				break
			}
		}

		if !excluded {
			filtered = append(filtered, proxyRaw)
		}
	}

	group["proxies"] = filtered
	return group
}

func prependArr(subscribe map[string]interface{}, subscribeKey string, parsers map[string]interface{}, parserKey string) map[string]interface{} {
	if arrToPrepend, arrToPrependExist := parsers[parserKey].([]interface{}); arrToPrependExist {
		log.Debugln("parses找到%s", parserKey)
		if originalArr, originalArrExist := subscribe[subscribeKey].([]interface{}); originalArrExist {
			log.Debugln("subscribe找到%s", subscribeKey)
			log.Debugln("subscribe原始%s:%v", subscribeKey, originalArr)
			originalArr = append(arrToPrepend, originalArr...)
			subscribe[subscribeKey] = originalArr
			log.Debugln("subscribe编辑后%s:%v", subscribeKey, subscribe[subscribeKey])
		} else {
			subscribe[subscribeKey] = arrToPrepend
			log.Debugln("subscribe编辑后%s:%v", subscribeKey, subscribe[subscribeKey])
		}
	} else {
		log.Debugln("parses未找到%s", parserKey)
	}
	return subscribe
}

func FetchAndValid(
	path string,
	url string,
	force bool,
	reportStatus func(string),
) error {
	configPath := P.Join(path, "config.yaml")

	if _, err := os.Stat(configPath); os.IsNotExist(err) || force {
		url, err := U.Parse(url)
		if err != nil {
			return err
		}

		bytes, _ := json.Marshal(&Status{
			Action:      "FetchConfiguration",
			Args:        []string{url.Host},
			Progress:    -1,
			MaxProgress: -1,
		})

		reportStatus(string(bytes))

		if err := fetch(url, configPath); err != nil {
			return err
		}
	}

	defer runtime.GC()

	rawCfg, err := UnmarshalAndPatch(path)
	if err != nil {
		return err
	}

	forEachProviders(rawCfg, func(index int, total int, name string, provider map[string]any, prefix string) {
		bytes, _ := json.Marshal(&Status{
			Action:      "FetchProviders",
			Args:        []string{name},
			Progress:    index,
			MaxProgress: total,
		})

		reportStatus(string(bytes))

		u, uok := provider["url"]
		p, pok := provider["path"]

		if !uok || !pok {
			return
		}

		us, uok := u.(string)
		ps, pok := p.(string)

		if !uok || !pok {
			return
		}

		if _, err := os.Stat(ps); err == nil {
			return
		}

		url, err := U.Parse(us)
		if err != nil {
			return
		}

		_ = fetch(url, ps)
	})

	bytes, _ := json.Marshal(&Status{
		Action:      "Verifying",
		Args:        []string{},
		Progress:    0xffff,
		MaxProgress: 0xffff,
	})

	reportStatus(string(bytes))

	cfg, err := Parse(rawCfg)
	if err != nil {
		return err
	}

	destroyProviders(cfg)

	return nil
}
