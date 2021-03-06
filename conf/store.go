/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2020 WireGuard LLC. All Rights Reserved.
 */

package conf

import (
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"golang.zx2c4.com/wireguard/windows/conf/dpapi"
)

const configFileSuffix = ".conf.dpapi"
const configFileUnencryptedSuffix = ".conf"

func ListConfigNames() ([]string, error) {
	configFileDir, err := tunnelConfigurationsDirectory()
	if err != nil {
		return nil, err
	}
	files, err := ioutil.ReadDir(configFileDir)
	if err != nil {
		return nil, err
	}
	configs := make([]string, len(files))
	i := 0
	for _, file := range files {
		name := filepath.Base(file.Name())
		if len(name) <= len(configFileSuffix) || !strings.HasSuffix(name, configFileSuffix) {
			continue
		}
		if !file.Mode().IsRegular() || file.Mode().Perm()&0444 == 0 {
			continue
		}
		name = strings.TrimSuffix(name, configFileSuffix)
		if !TunnelNameIsValid(name) {
			continue
		}
		configs[i] = name
		i++
	}
	return configs[:i], nil
}

var migrating sync.Mutex
var lastMigrationTimer *time.Timer

func MigrateUnencryptedConfigs(sharingBase int) (int, []error) {
	migrating.Lock()
	defer migrating.Unlock()
	configFileDir, err := tunnelConfigurationsDirectory()
	if err != nil {
		return 0, []error{err}
	}
	files, err := ioutil.ReadDir(configFileDir)
	if err != nil {
		return 0, []error{err}
	}
	errs := make([]error, len(files))
	i := 0
	e := 0
	for _, file := range files {
		path := filepath.Join(configFileDir, file.Name())
		name := filepath.Base(file.Name())
		if len(name) <= len(configFileUnencryptedSuffix) || !strings.HasSuffix(name, configFileUnencryptedSuffix) {
			continue
		}
		if !file.Mode().IsRegular() || file.Mode().Perm()&0444 == 0 {
			continue
		}

		// We don't use ioutil's ReadFile, because we actually want RDWR, so that we can take advantage
		// of Windows file locking for ensuring the file is finished being written.
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			if sharingBase > 0 && errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				if lastMigrationTimer != nil {
					lastMigrationTimer.Stop()
				}
				lastMigrationTimer = time.AfterFunc(time.Second/time.Duration(sharingBase*sharingBase), func() { MigrateUnencryptedConfigs(sharingBase - 1) })
				sharingBase = 0
			}
			errs[e] = err
			e++
			continue
		}
		bytes, err := ioutil.ReadAll(f)
		f.Close()
		if err != nil {
			errs[e] = err
			e++
			continue
		}
		config, err := FromWgQuickWithUnknownEncoding(string(bytes), strings.TrimSuffix(name, configFileUnencryptedSuffix))
		if err != nil {
			errs[e] = err
			e++
			continue
		}
		err = config.Save(false)
		if err != nil {
			errs[e] = err
			e++
			continue
		}
		err = os.Remove(path)
		if err != nil {
			errs[e] = err
			e++
			continue
		}
		i++
	}
	return i, errs[:e]
}

func LoadFromName(name string) (*Config, error) {
	configFileDir, err := tunnelConfigurationsDirectory()
	if err != nil {
		return nil, err
	}
	return LoadFromPath(filepath.Join(configFileDir, name+configFileSuffix))
}

func LoadFromPath(path string) (*Config, error) {
	name, err := NameFromPath(path)
	if err != nil {
		return nil, err
	}
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, configFileSuffix) {
		bytes, err = dpapi.Decrypt(bytes, name)
		if err != nil {
			return nil, err
		}
	}
	return FromWgQuickWithUnknownEncoding(string(bytes), name)
}

func PathIsEncrypted(path string) bool {
	return strings.HasSuffix(filepath.Base(path), configFileSuffix)
}

func NameFromPath(path string) (string, error) {
	name := filepath.Base(path)
	if !((len(name) > len(configFileSuffix) && strings.HasSuffix(name, configFileSuffix)) ||
		(len(name) > len(configFileUnencryptedSuffix) && strings.HasSuffix(name, configFileUnencryptedSuffix))) {
		return "", errors.New("Path must end in either " + configFileSuffix + " or " + configFileUnencryptedSuffix)
	}
	if strings.HasSuffix(path, configFileSuffix) {
		name = strings.TrimSuffix(name, configFileSuffix)
	} else {
		name = strings.TrimSuffix(name, configFileUnencryptedSuffix)
	}
	if !TunnelNameIsValid(name) {
		return "", errors.New("Tunnel name is not valid")
	}
	return name, nil
}

func (config *Config) Save(overwrite bool) error {
	if !TunnelNameIsValid(config.Name) {
		return errors.New("Tunnel name is not valid")
	}
	configFileDir, err := tunnelConfigurationsDirectory()
	if err != nil {
		return err
	}
	filename := filepath.Join(configFileDir, config.Name+configFileSuffix)
	bytes := []byte(config.ToWgQuick())
	bytes, err = dpapi.Encrypt(bytes, config.Name)
	if err != nil {
		return err
	}
	return writeLockedDownFile(filename, overwrite, bytes)
}

func (config *Config) Path() (string, error) {
	if !TunnelNameIsValid(config.Name) {
		return "", errors.New("Tunnel name is not valid")
	}
	configFileDir, err := tunnelConfigurationsDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(configFileDir, config.Name+configFileSuffix), nil
}

func DeleteName(name string) error {
	if !TunnelNameIsValid(name) {
		return errors.New("Tunnel name is not valid")
	}
	configFileDir, err := tunnelConfigurationsDirectory()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(configFileDir, name+configFileSuffix))
}

func (config *Config) Delete() error {
	return DeleteName(config.Name)
}
