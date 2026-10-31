package protobuf

import (
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/encoding/protowire"
)

// plugHeartbeatField describes a known field in the plug_heartbeat protobuf message.
// Schema reverse-engineered from foxthefox/ioBroker.ecoflow-mqtt.
type plugHeartbeatField struct {
	name       string
	multiplier float64
}

// plugHeartbeatFields maps protobuf field numbers to metric names and scaling factors.
// Source: https://github.com/foxthefox/ioBroker.ecoflow-mqtt
//
//	watts (field 10): raw value is deci-watts  → multiply by 0.1 to get W
//	current (field 8): raw value is milli-amps → multiply by 0.001 to get A
//	volt (field 9):   raw value is Volts        → no scaling
//	temp (field 6):   raw value is °C           → no scaling
//	freq (field 7):   raw value is Hz           → no scaling
var plugHeartbeatFields = map[protowire.Number]plugHeartbeatField{
	5:  {"maxCur", 0.1},        // Max current (A)
	6:  {"temp", 1.0},          // Temperature (°C)
	7:  {"freq", 1.0},          // AC frequency (Hz)
	8:  {"current", 0.001},     // AC current (A, raw = mA)
	9:  {"volt", 1.0},          // AC voltage (V)
	10: {"watts", 0.1},         // AC power (W, raw = dW)
	11: {"switch", 1.0},        // Switch state: 0=off 1=on
	12: {"brightness", 1.0},    // LED brightness (0–1023)
	13: {"maxWatts", 1.0},      // Max watt limit (W)
	19: {"uptime", 1.0},        // Device uptime (s)
	34: {"powerPlugs", 0.1},    // Power to plugs when paired with PowerStream (W, raw = dW)
	39: {"dynWattEnable", 1.0}, // Dynamic watt enable (0/1)
}

// decodePlugHeartbeat decodes a plug_heartbeat pdata (cmd_func=2) from an EcoFlow smart plug.
// Returns a map of metric name → float64 value with multipliers applied.
// Returns an error if no known fields were decoded.
func decodePlugHeartbeat(pdata []byte) (map[string]interface{}, error) {
	params := make(map[string]interface{})
	b := pdata

	for len(b) > 0 {
		fieldNum, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return params, fmt.Errorf("plug_heartbeat: invalid tag at offset %d", len(pdata)-len(b))
		}
		b = b[n:]

		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return params, fmt.Errorf("plug_heartbeat: invalid varint for field %d", fieldNum)
			}
			b = b[n:]
			if meta, ok := plugHeartbeatFields[fieldNum]; ok {
				// Cast to int64 so negative int32 values (sign-extended to 10 bytes) are handled
				// correctly. Multiplying a negative sentinel value (e.g. powerPlugs when not
				// paired with a PowerStream) still produces a sensible float.
				params[meta.name] = float64(int64(v)) * meta.multiplier
			}

		case protowire.BytesType:
			_, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return params, fmt.Errorf("plug_heartbeat: invalid bytes for field %d", fieldNum)
			}
			b = b[n:]
			// No byte-type fields are currently mapped; skip silently.

		case protowire.Fixed32Type:
			_, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return params, fmt.Errorf("plug_heartbeat: invalid fixed32 for field %d", fieldNum)
			}
			b = b[n:]

		case protowire.Fixed64Type:
			_, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return params, fmt.Errorf("plug_heartbeat: invalid fixed64 for field %d", fieldNum)
			}
			b = b[n:]

		default:
			return params, fmt.Errorf("plug_heartbeat: unsupported wire type %d for field %d", typ, fieldNum)
		}
	}

	if len(params) == 0 {
		return nil, fmt.Errorf("plug_heartbeat: no known fields found in pdata")
	}
	return params, nil
}

// decodeEnergyPack decodes an EnergyPack pdata (cmd_func=254, cmd_id=32) from an EcoFlow smart plug.
// Returns aggregate daily metrics:
//   - "dailyEnergyWh"  – total energy consumed today (Wh)
//   - "dailyUptimeMin" – total minutes the plug was on today
//
// EnergyPack contains 24 hourly buckets; watth_type=5 is Wh per hour,
// watth_type=6 is on-duration in minutes per hour.
func decodeEnergyPack(pdata []byte) (map[string]interface{}, error) {
	params := make(map[string]interface{})

	// Outer fields: field 1 = sys_seq (uint32), field 2 = repeated EnergyItem (bytes)
	b := pdata
	for len(b) > 0 {
		fieldNum, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return params, fmt.Errorf("energy_pack: invalid tag")
		}
		b = b[n:]

		switch {
		case typ == protowire.VarintType:
			_, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return params, fmt.Errorf("energy_pack: invalid varint for field %d", fieldNum)
			}
			b = b[n:]
			// field 1 (sys_seq) – we don't need it for metrics

		case typ == protowire.BytesType && fieldNum == 2:
			// EnergyItem sub-message
			itemBytes, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return params, fmt.Errorf("energy_pack: invalid bytes for EnergyItem")
			}
			b = b[n:]

			watthType, watth := parseEnergyItem(itemBytes)
			switch watthType {
			case 5: // Wh per hour
				var total float64
				for _, wh := range watth {
					total += float64(wh)
				}
				params["dailyEnergyWh"] = total
			case 6: // on-duration minutes per hour
				var total float64
				for _, m := range watth {
					total += float64(m)
				}
				params["dailyUptimeMin"] = total
			}

		case typ == protowire.BytesType:
			_, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return params, fmt.Errorf("energy_pack: invalid bytes for field %d", fieldNum)
			}
			b = b[n:]

		default:
			// Skip any other wire types
			n := protowire.ConsumeFieldValue(fieldNum, typ, b)
			if n < 0 {
				return params, fmt.Errorf("energy_pack: could not skip field %d (wire type %d)", fieldNum, typ)
			}
			b = b[n:]
		}
	}

	if len(params) == 0 {
		return nil, fmt.Errorf("energy_pack: no metrics extracted")
	}
	return params, nil
}

// parseEnergyItem decodes an EnergyItem sub-message and returns (watth_type, watth_values).
// field 2 = watth_type (uint32: 5=Wh, 6=on-duration-min)
// field 3 = repeated uint32 watth (packed, 24 hourly values)
func parseEnergyItem(b []byte) (watthType uint64, watth []uint64) {
	for len(b) > 0 {
		fieldNum, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return
		}
		b = b[n:]

		switch {
		case typ == protowire.VarintType && fieldNum == 2:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return
			}
			b = b[n:]
			watthType = v

		case typ == protowire.BytesType && fieldNum == 3:
			// Packed repeated uint32 (protobuf packed encoding)
			raw, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return
			}
			b = b[n:]
			rb := raw
			for len(rb) > 0 {
				v, vn := protowire.ConsumeVarint(rb)
				if vn < 0 {
					break
				}
				rb = rb[vn:]
				watth = append(watth, v)
			}

		default:
			n := protowire.ConsumeFieldValue(fieldNum, typ, b)
			if n < 0 {
				return
			}
			b = b[n:]
		}
	}
	return
}

// logSmartPlugDecode logs the result of a smart plug protobuf decode attempt (debug only).
func logSmartPlugDecode(decoder string, cmdFunc, cmdId int32, params map[string]interface{}, err error) {
	if !isProtobufDebugEnabled() {
		return
	}
	if err != nil {
		slog.Debug("[PROTOBUF_DEBUG] Smart plug decode failed",
			"decoder", decoder,
			"cmd_func", cmdFunc,
			"cmd_id", cmdId,
			"error", err)
	} else {
		slog.Info("[PROTOBUF_DEBUG] Smart plug decode succeeded",
			"decoder", decoder,
			"cmd_func", cmdFunc,
			"cmd_id", cmdId,
			"param_count", len(params))
	}
}
