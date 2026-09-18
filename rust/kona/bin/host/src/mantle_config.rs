//! `[MANTLE]` Guards against a rollup config scheduling a Mantle hardfork this build does not
//! implement.
//!
//! `MantleHardForkConfig` carries `#[serde(deny_unknown_fields)]`, which looks like it already
//! covers this. It does not: `RollupConfig` embeds the struct with `#[serde(flatten)]`, and
//! serde documents `deny_unknown_fields` as unsupported in combination with `flatten`. On the
//! real path the attribute is inert, so an unrecognised `mantle_*_time` key is accepted and
//! dropped — op-node would activate that fork, kona would not, and nothing would say so.
//!
//! Hence this scan, at the two places a `RollupConfig` is read from disk.

use kona_genesis::MantleHardForkConfig;

/// Returns the `mantle_*_time` keys in `raw` that this build does not implement.
///
/// Takes the already-parsed JSON so the caller does not pay for a second parse. Non-object
/// input yields nothing: the deserializer will have failed on it already.
pub(crate) fn unknown_mantle_forks(raw: &serde_json::Value) -> Vec<String> {
    let Some(map) = raw.as_object() else { return Vec::new() };
    map.keys()
        .filter(|k| {
            MantleHardForkConfig::looks_like_time_key(k) &&
                !MantleHardForkConfig::is_known_time_key(k)
        })
        .cloned()
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn flags_only_unimplemented_mantle_forks() {
        let raw = json!({
            "block_time": 2,
            "mantle_skadi_time": 10,
            "mantle_arsia_time": 20,
            "mantle_elysium_time": 30,
            "mantle_someday_time": 40,
            "mantle_other_future_time": 50,
            "some_unrelated_key": 1,
            // Does not match the `mantle_*_time` shape, so it is not our business.
            "mantle_not_a_fork": 2,
        });
        let mut found = unknown_mantle_forks(&raw);
        found.sort();
        assert_eq!(found, vec!["mantle_other_future_time", "mantle_someday_time"]);
    }

    #[test]
    fn every_implemented_fork_passes() {
        let raw = serde_json::Value::Object(
            MantleHardForkConfig::TIME_KEYS.iter().map(|k| ((*k).to_string(), json!(1))).collect(),
        );
        assert!(unknown_mantle_forks(&raw).is_empty());
    }

    #[test]
    fn non_object_input_is_not_an_error() {
        assert!(unknown_mantle_forks(&json!([1, 2, 3])).is_empty());
        assert!(unknown_mantle_forks(&json!(null)).is_empty());
    }
}
