package host_sync

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"time"

	"regexp"

	"encoding/hex"

	redis "gopkg.in/redis.v3"
)

var dockerAPI string = ""
var redisClient *redis.Client
var redisPrefix string = "__yar_host_sync__:"

var hostCheckSum map[string]string

func init() {
	hostCheckSum = make(map[string]string)
}

func SetDockerAPI(api string) {
	dockerAPI = api
}

func SetRedisHost(host string) {
	if redisClient != nil {
		redisClient.Close()
	}

	opt := &redis.Options{}
	opt.Addr = host
	opt.DB = 7
	opt.IdleTimeout = 60 * time.Second
	opt.WriteTimeout = 10 * time.Second
	opt.ReadTimeout = 10 * time.Second
	opt.MaxRetries = 3
	redisClient = redis.NewClient(opt)
}

func GetHostListFromDockerAPI(pool string, name string) ([]string, error) {

	if len(dockerAPI) < 1 {
		return nil, errors.New("Please Call SetDockerAPI()")
	}

	filters := map[string][]string{
		"label": []string{
			fmt.Sprintf(`com.docker.swarm.constraints=["pool==%s"]`, pool),
			fmt.Sprintf("wxhost-service-name=%s", name),
		},
	}

	query, err := json.Marshal(filters)

	if err != nil {
		return nil, err
	}

	api := fmt.Sprintf(`%s/containers/json?filters=%s`, dockerAPI, url.QueryEscape(string(query)))

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	tr.DisableKeepAlives = true

	httpClient := &http.Client{}
	httpClient.Timeout = 5 * time.Second
	httpClient.Transport = tr

	resp, err := httpClient.Get(api)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return nil, err
	}

	var list []string

	var lstContainers []map[string]interface{}

	err = json.Unmarshal(body, &lstContainers)

	if err != nil {
		return nil, err
	}

	for _, item := range lstContainers {
		ports, ok := item["Ports"].([]interface{})
		if ok {
			if len(ports) > 0 {
				p, ok := ports[0].(map[string]interface{})
				if ok {
					ip, ok1 := p["IP"]
					port, ok2 := p["PublicPort"]
					if ok1 && ok2 {
						h := fmt.Sprintf("%s:%s", fmt.Sprint(ip), fmt.Sprint(port))
						list = append(list, h)
					}
				}
			}
		}
	}
	SetHostListToRedis(pool, name, list)
	return list, nil
}

func GetHostListFromRedis(pool string, name string) ([]string, error) {

	if redisClient == nil {
		return nil, errors.New("Please Call SetRedisHost()")
	}

	key := fmt.Sprintf("%s%s:%s", redisPrefix, pool, name)
	ret := redisClient.Get(key)

	if ret.Err() != nil {
		log.Println("[GetHostListFromRedis]:", ret.Err())
		return nil, ret.Err()
	}

	val := ret.Val()
	log.Println(val)
	var host []string
	err := json.Unmarshal([]byte(val), &host)
	return host, err
}

func SetHostListToRedis(pool, name string, list []string) error {
	if redisClient == nil {
		return errors.New("Please Call SetRedisHost()")
	}

	jsonStr, err := json.Marshal(list)

	if err != nil {
		return err
	}

	key := fmt.Sprintf("%s%s:%s", redisPrefix, pool, name)
	ret := redisClient.Set(key, jsonStr, 3600*24*7*time.Second)

	if ret.Err() != nil {
		return ret.Err()
	}
	return nil
}

func SyncAllHostList() error {

	api := fmt.Sprintf(`%s/containers/json`, dockerAPI)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	tr.DisableKeepAlives = true

	httpClient := &http.Client{}
	httpClient.Timeout = 5 * time.Second
	httpClient.Transport = tr

	resp, err := httpClient.Get(api)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return err
	}

	var list map[string]map[string][]string = make(map[string]map[string][]string)

	var lstContainers []map[string]interface{}

	err = json.Unmarshal(body, &lstContainers)

	if err != nil {
		return err
	}

	for _, item := range lstContainers {

		labels, ok := item["Labels"].(map[string]interface{})

		if !ok {
			continue
		}

		service, ok := labels["com.docker.compose.service"].(string)
		if !ok {
			continue
		}
		pool, ok := labels["com.docker.swarm.constraints"].(string)

		if !ok {
			continue
		}

		wxhostService, ok := labels["wxhost-service-name"].(string)

		if ok && len(wxhostService) > 0 {
			service = wxhostService
		}

		reg := regexp.MustCompile("pool\\=\\=(\\w+)")
		p := reg.FindAllStringSubmatch(pool, -1)
		if len(p) > 0 {
			pool = p[0][1]
		}

		if _, ok := list[pool]; !ok {
			list[pool] = make(map[string][]string)
		}

		ports, ok := item["Ports"].([]interface{})
		if ok {
			if len(ports) > 0 {
				p, ok := ports[0].(map[string]interface{})
				if ok {
					ip, ok1 := p["IP"]
					port, ok2 := p["PublicPort"]
					if ok1 && ok2 {
						h := fmt.Sprintf("%s:%s", fmt.Sprint(ip), fmt.Sprint(port))
						list[pool][service] = append(list[pool][service], h)
					}
				}
			}
		}
	}

	for pool, lst1 := range list {
		changed := 0
		for service, hostList := range lst1 {
			key := fmt.Sprintf("%s_%s", pool, service)
			sum, ok := hostCheckSum[key]
			n, _ := json.Marshal(hostList)
			s := hex.EncodeToString(n[:])
			if sum == s {
				continue
			}
			if ok && sum == s {
				continue
			}
			SetHostListToRedis(pool, service, hostList)
			hostCheckSum[key] = s
			changed++
		}
		log.Printf("[SyncAllHostList] %s services:%d changed:%d", pool, len(lst1), changed)
	}
	return nil
}