package vyos

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
)

type APIClient struct {
	Host   string
	Apikey string
	Client *http.Client
}

func NewAPIClient(host, apikey string) (*APIClient, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	return &APIClient{
		Host:   host,
		Apikey: apikey,
		Client: &http.Client{Transport: tr},
	}, nil
}

type Command struct {
	Op          string   `json:"op"`
	Path        []string `json:"path"`
	Value       string   `json:"value,omitempty"`
	ForceDelete bool     `json:"-"`
}

func IsRoutePath(path []string) bool {
	for i, part := range path {
		if i > 0 && part == "route" && path[i-1] == "static" {
			return true
		}
	}
	return false
}

func GetRoutePrefix(path []string) string {
	for i, part := range path {
		if part == "route" && i < len(path)-1 {
			return path[i+1]
		}
	}
	return ""
}
func (c *APIClient) ApplyCommands(commands []Command) error {
	endpoint := fmt.Sprintf("%s/configure", c.Host)

	payload := map[string]interface{}{
		"commands": []map[string]interface{}{},
		"key":      c.Apikey,
	}

	for _, cmd := range commands {
		command := map[string]interface{}{
			"op":   cmd.Op,
			"path": cmd.Path,
		}
		if cmd.Value != "" {
			command["value"] = cmd.Value
		}
		payload["commands"] = append(payload["commands"].([]map[string]interface{}), command)
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("erro: %w", err)
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return fmt.Errorf("erro to create a request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("erro: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func (c *APIClient) GetCurrentConfig(path []string) (map[string]interface{}, error) {
	endpoint := fmt.Sprintf("%s/retrieve", c.Host)
	requestData := map[string]interface{}{
		"op":   "showConfig",
		"path": path,
	}

	formData := map[string]string{
		"data": mustMarshal(requestData),
		"key":  c.Apikey,
	}

	resp, err := c.postForm(endpoint, formData)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode showConfig response: %v", err)
	}

	return result.Data, nil
}

func (c *APIClient) postForm(url string, data map[string]string) (*http.Response, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormField("data")
	if err != nil {
		return nil, err
	}
	part.Write([]byte(data["data"]))

	part, err = writer.CreateFormField("key")
	if err != nil {
		return nil, err
	}
	part.Write([]byte(data["key"]))

	writer.Close()

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	return c.Client.Do(req)
}

func parseAPIResponse(resp *http.Response) error {
	var result struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}

	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}

	if !result.Success {
		return fmt.Errorf("API error: %s", result.Error)
	}
	return nil
}

func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (c *APIClient) PathExists(path []string) (bool, error) {
	endpoint := fmt.Sprintf("%s/retrieve", c.Host)
	requestData := map[string]interface{}{
		"op":   "exists",
		"path": path,
	}

	formData := map[string]string{
		"data": mustMarshal(requestData),
		"key":  c.Apikey,
	}

	resp, err := c.postForm(endpoint, formData)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Data bool `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	return result.Data, nil
}

func (c *APIClient) CountNextHops(routePath []string) (int, error) {
	if len(routePath) != 4 || routePath[0] != "protocols" || routePath[1] != "static" || routePath[2] != "route" {
		return 0, fmt.Errorf("invalid path: %v", routePath)
	}

	config, err := c.GetCurrentConfig(append(routePath, "next-hop"))
	if err != nil {
		return 0, err
	}

	if nextHops, ok := config["next-hop"].(map[string]interface{}); ok {
		return len(nextHops), nil
	}
	return 0, nil
}

func (c *APIClient) GetPathValue(path []string) (string, error) {
	if IsRoutePath(path) && len(path) >= 5 && path[len(path)-2] == "next-hop" {
		return path[len(path)-1], nil
	}

	if IsRoutePath(path) && len(path) >= 4 && path[len(path)-1] == "next-hop" {
		config, err := c.GetCurrentConfig(path)
		if err != nil {
			return "", err
		}
		if nextHops, ok := config["next-hop"].(map[string]interface{}); ok {
			if len(nextHops) == 1 {
				for ip := range nextHops {
					return ip, nil
				}
			} else {
				bytes, _ := json.Marshal(config)
				return string(bytes), nil
			}
		}
	}

	config, err := c.GetCurrentConfig(path)
	if err != nil {
		return "", err
	}

	if len(path) > 0 && path[len(path)-1] == "address" {
		if addresses, ok := config["address"].([]interface{}); ok {
			sort.Slice(addresses, func(i, j int) bool {
				return fmt.Sprintf("%v", addresses[i]) < fmt.Sprintf("%v", addresses[j])
			})
			valueMap := map[string]interface{}{"address": addresses}
			bytes, _ := json.Marshal(valueMap)
			return string(bytes), nil
		}
	}

	return extractConfigValue(config), nil
}
func (c *APIClient) GetNextHops(routePath []string) ([]string, error) {
	if len(routePath) != 4 || routePath[0] != "protocols" || routePath[1] != "static" || routePath[2] != "route" {
		return nil, fmt.Errorf("invalid path: %v", routePath)
	}

	config, err := c.GetCurrentConfig(append(routePath, "next-hop"))
	if err != nil {
		return nil, err
	}

	nextHops := make([]string, 0)
	if nh, ok := config["next-hop"].(map[string]interface{}); ok {
		for ip := range nh {
			nextHops = append(nextHops, ip)
		}
	}
	return nextHops, nil
}
