// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

const (
	maxSequenceLoopIterations = 1000
	maxPlannedInstructions    = 1000
	maxSequenceNestingDepth   = 64
)

const (
	sequentialContainerType = "OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer"
	loopConditionType       = "OpenAstroAra.Sequencer.Conditions.LoopCondition, OpenAstroAra.Sequencer"
	switchFilterType        = "OpenAstroAra.Sequencer.SequenceItem.FilterWheel.SwitchFilter, OpenAstroAra.Sequencer"
	takeExposureType        = "NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer"
	annotationType          = "OpenAstroAra.Sequencer.SequenceItem.Utility.Annotation, OpenAstroAra.Sequencer"
)

func validateSupportedSequenceBody(body jsontext.Value) error {
	root, err := sequenceObject(body)
	if err != nil {
		return err
	}
	version, ok := sequenceString(root["schemaVersion"])
	if !ok || version != "openastroara-sequence-v1" {
		return errors.New("schemaVersion must be openastroara-sequence-v1")
	}
	planned := 0
	if err := inspectSequentialContainer(root, 1, 1, &planned); err != nil {
		return err
	}
	if planned == 0 {
		return errors.New("sequence must contain a supported TakeExposure instruction")
	}
	return nil
}

func inspectSequentialContainer(container map[string]jsontext.Value, multiplier, depth int, planned *int) error {
	if depth > maxSequenceNestingDepth {
		return fmt.Errorf("sequence container nesting exceeds %d levels", maxSequenceNestingDepth)
	}
	if typeName, _ := sequenceString(container["$type"]); typeName != sequentialContainerType {
		return fmt.Errorf("unsupported container type %q", boundedSequenceText(typeName))
	}
	conditions, err := sequenceCollection(container["Conditions"], true)
	if err != nil {
		return fmt.Errorf("invalid Conditions: %w", err)
	}
	loopCount := 0
	for _, rawCondition := range conditions {
		condition, err := sequenceObject(rawCondition)
		if err != nil {
			return fmt.Errorf("invalid sequence condition: %w", err)
		}
		typeName, _ := sequenceString(condition["$type"])
		if typeName != loopConditionType {
			return fmt.Errorf("unsupported sequence condition type %q", boundedSequenceText(typeName))
		}
		iterations, ok := sequenceInt(condition["Iterations"])
		if !ok || iterations < 1 || iterations > maxSequenceLoopIterations {
			return fmt.Errorf("LoopCondition.Iterations must be between 1 and %d", maxSequenceLoopIterations)
		}
		loopCount++
		if loopCount > 1 {
			return errors.New("only one LoopCondition per container is supported")
		}
		if multiplier > maxPlannedInstructions/iterations {
			return fmt.Errorf("nested loop expansion exceeds %d planned instructions", maxPlannedInstructions)
		}
		multiplier *= iterations
	}
	triggers, err := sequenceCollection(container["Triggers"], true)
	if err != nil {
		return fmt.Errorf("invalid Triggers: %w", err)
	}
	if len(triggers) != 0 {
		return errors.New("sequence triggers are not in the verified palette")
	}
	items, err := sequenceCollection(container["Items"], false)
	if err != nil {
		return fmt.Errorf("invalid Items: %w", err)
	}
	for _, rawItem := range items {
		item, err := sequenceObject(rawItem)
		if err != nil {
			return fmt.Errorf("invalid sequence item: %w", err)
		}
		if value, exists := item["ContinueOnError"]; exists {
			var continueOnError bool
			if err := json.Unmarshal(value, &continueOnError); err != nil {
				return errors.New("ContinueOnError must be a boolean")
			}
			if continueOnError {
				return errors.New("ContinueOnError is unsupported because Ara can report a completed run after instruction_failed")
			}
		}
		typeName, _ := sequenceString(item["$type"])
		*planned += multiplier
		if *planned > maxPlannedInstructions {
			return fmt.Errorf("sequence exceeds %d planned instructions", maxPlannedInstructions)
		}
		switch typeName {
		case sequentialContainerType:
			if err := inspectSequentialContainer(item, multiplier, depth+1, planned); err != nil {
				return err
			}
		case switchFilterType:
			if err := validateSwitchFilter(item); err != nil {
				return err
			}
		case takeExposureType:
			if err := validateTakeExposure(item); err != nil {
				return err
			}
		case annotationType:
		default:
			return fmt.Errorf("unsupported executable sequence type %q", boundedSequenceText(typeName))
		}
	}
	return nil
}

func boundedSequenceText(value string) string {
	if len(value) <= 160 {
		return value
	}
	return strings.ToValidUTF8(value[:160], "�") + "…"
}

func validateSwitchFilter(item map[string]jsontext.Value) error {
	filter, err := sequenceObject(item["Filter"])
	if err != nil {
		return errors.New("SwitchFilter requires a filter object")
	}
	name, nameOK := sequenceString(filter["_name"])
	position, positionOK := sequenceInt(filter["_position"])
	if !nameOK || strings.TrimSpace(name) == "" || !positionOK || position < 0 {
		return errors.New("SwitchFilter requires a non-empty filter name and non-negative slot position")
	}
	return nil
}

func validateTakeExposure(item map[string]jsontext.Value) error {
	exposure, ok := sequenceFloat(item["ExposureTime"])
	if !ok || exposure <= 0 || math.IsInf(exposure, 0) || math.IsNaN(exposure) {
		return errors.New("TakeExposure.ExposureTime must be a finite positive number of seconds")
	}
	if imageType, exists := item["ImageType"]; exists {
		value, ok := sequenceString(imageType)
		if !ok || !slices.Contains([]string{"LIGHT", "FLAT", "DARK", "BIAS", "SNAPSHOT", "DARKFLAT"}, strings.ToUpper(value)) {
			return errors.New("TakeExposure.ImageType is unsupported")
		}
	}
	if rawBinning, exists := item["Binning"]; exists && rawBinning.Kind() != 'n' {
		binning, err := sequenceObject(rawBinning)
		if err != nil {
			return errors.New("TakeExposure.Binning must be an object")
		}
		for _, axis := range []string{"X", "Y"} {
			value, ok := sequenceInt(binning[axis])
			if !ok || value < 1 {
				return fmt.Errorf("TakeExposure.Binning.%s must be a positive integer", axis)
			}
		}
	}
	for _, field := range []string{"Gain", "Offset"} {
		if value, exists := item[field]; exists {
			number, ok := sequenceInt(value)
			if !ok || number < -1 {
				return fmt.Errorf("TakeExposure.%s must be -1 or a non-negative integer", field)
			}
		}
	}
	return nil
}

func sequenceObject(value jsontext.Value) (map[string]jsontext.Value, error) {
	if len(value) == 0 || value.Kind() != '{' {
		return nil, errors.New("expected a JSON object")
	}
	var object map[string]jsontext.Value
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func sequenceCollection(value jsontext.Value, optional bool) ([]jsontext.Value, error) {
	if len(value) == 0 || value.Kind() == 'n' {
		if optional {
			return nil, nil
		}
		return nil, errors.New("collection is required")
	}
	if value.Kind() == '[' {
		var items []jsontext.Value
		if err := json.Unmarshal(value, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	collection, err := sequenceObject(value)
	if err != nil {
		return nil, err
	}
	values, ok := collection["$values"]
	if !ok || values.Kind() != '[' {
		return nil, errors.New("expected an array or Ara $values collection")
	}
	var items []jsontext.Value
	if err := json.Unmarshal(values, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func sequenceString(value jsontext.Value) (string, bool) {
	if len(value) == 0 || value.Kind() != '"' {
		return "", false
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", false
	}
	return result, true
}

func sequenceInt(value jsontext.Value) (int, bool) {
	if len(value) == 0 || value.Kind() != '0' {
		return 0, false
	}
	result, err := strconv.Atoi(strings.TrimSpace(value.String()))
	return result, err == nil
}

func sequenceFloat(value jsontext.Value) (float64, bool) {
	if len(value) == 0 || value.Kind() != '0' {
		return 0, false
	}
	result, err := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
	return result, err == nil
}
