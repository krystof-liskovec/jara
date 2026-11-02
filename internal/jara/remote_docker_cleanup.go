package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultDockerCleanupAge = 24 * time.Hour

type staleDockerContainer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image"`
	Age   string `json:"age"`
}

func dockerCleanupAge(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultDockerCleanupAge, nil
	}
	age, err := time.ParseDuration(raw)
	if err != nil || age < time.Hour || age > 365*24*time.Hour {
		return 0, fmt.Errorf("--older-than must be from 1h to 8760h")
	}
	return age, nil
}

func staleDockerContainers(olderThan time.Duration) ([]staleDockerContainer, error) {
	if os.Getenv("JARA_DOCKER") != "1" {
		return nil, fmt.Errorf("nested Docker is not enabled for this Jara environment")
	}
	idsOutput, err := exec.Command("docker", "container", "ls", "--quiet", "--no-trunc", "--filter", "status=exited").Output()
	if err != nil {
		return nil, fmt.Errorf("list stopped Docker containers: %w", err)
	}
	ids := strings.Fields(string(idsOutput))
	if len(ids) == 0 {
		return []staleDockerContainer{}, nil
	}
	args := append([]string{"container", "inspect"}, ids...)
	data, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect stopped Docker containers: %w", err)
	}
	var inspected []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
		State struct {
			Status     string `json:"Status"`
			FinishedAt string `json:"FinishedAt"`
		} `json:"State"`
	}
	if err = json.Unmarshal(data, &inspected); err != nil {
		return nil, fmt.Errorf("decode Docker container details: %w", err)
	}
	now := time.Now()
	var stale []staleDockerContainer
	for _, item := range inspected {
		if item.State.Status != "exited" || item.ID == "" {
			continue
		}
		finished, parseErr := time.Parse(time.RFC3339Nano, item.State.FinishedAt)
		if parseErr != nil || finished.IsZero() || now.Sub(finished) < olderThan {
			continue
		}
		age := now.Sub(finished).Round(time.Hour)
		stale = append(stale, staleDockerContainer{
			ID: item.ID, Name: strings.TrimPrefix(item.Name, "/"), Image: item.Config.Image, Age: age.String(),
		})
	}
	return stale, nil
}

func removeStaleDockerContainers(olderThan time.Duration, selected []string) error {
	if len(selected) == 0 || len(selected) > 1000 {
		return fmt.Errorf("select between 1 and 1000 stopped containers")
	}
	current, err := staleDockerContainers(olderThan)
	if err != nil {
		return err
	}
	eligible := make(map[string]bool, len(current))
	for _, container := range current {
		eligible[container.ID] = true
	}
	args := []string{"container", "rm"}
	seen := make(map[string]bool, len(selected))
	for _, id := range selected {
		if !eligible[id] || seen[id] || strings.HasPrefix(id, "-") {
			return fmt.Errorf("stopped containers changed; refresh the cleanup list and retry")
		}
		seen[id] = true
		args = append(args, id)
	}
	if output, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("remove stopped Docker containers: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
