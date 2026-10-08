//! Contains the Mantle-specific hardfork configuration for the chain.

/// `[MANTLE]` A Mantle hardfork schedule that op-node would reject at startup.
#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
pub enum MantleForkOrderError {
    /// A hardfork is scheduled while an earlier one is not.
    #[error("mantle fork {fork} set (to {activation}), but prior fork {prior} missing")]
    MissingPriorFork {
        /// The scheduled fork.
        fork: &'static str,
        /// Its activation timestamp.
        activation: u64,
        /// The unscheduled fork that must come first.
        prior: &'static str,
    },
    /// A hardfork is scheduled before an earlier one.
    #[error(
        "mantle fork {fork} set to {activation}, but prior fork {prior} has higher offset {prior_activation}"
    )]
    OutOfOrder {
        /// The out-of-order fork.
        fork: &'static str,
        /// Its activation timestamp.
        activation: u64,
        /// The fork that must come first.
        prior: &'static str,
        /// The prior fork's (later) activation timestamp.
        prior_activation: u64,
    },
}

/// Mantle-specific hardfork configuration.
#[derive(Debug, Copy, Clone, Default, Hash, Eq, PartialEq)]
#[cfg_attr(feature = "arbitrary", derive(arbitrary::Arbitrary))]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
#[cfg_attr(feature = "serde", serde(deny_unknown_fields))]
pub struct MantleHardForkConfig {
    /// `mantle_base_fee_time` sets the activation time for the Mantle `BaseFee` network upgrade.
    /// Active if `mantle_base_fee_time` != None && L2 block timestamp >=
    /// `Some(mantle_base_fee_time)`, inactive otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_base_fee_time: Option<u64>,
    /// `mantle_everest_time` sets the activation time for the Mantle Everest network upgrade.
    /// Active if `mantle_everest_time` != None && L2 block timestamp >=
    /// `Some(mantle_everest_time)`, inactive otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_everest_time: Option<u64>,
    /// `mantle_euboea_time` sets the activation time for the Mantle Euboea network upgrade.
    /// Active if `mantle_euboea_time` != None && L2 block timestamp >= `Some(mantle_euboea_time)`,
    /// inactive otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_euboea_time: Option<u64>,
    /// `mantle_skadi_time` sets the activation time for the Mantle Skadi network upgrade.
    /// Active if `mantle_skadi_time` != None && L2 block timestamp >= `Some(mantle_skadi_time)`,
    /// inactive otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_skadi_time: Option<u64>,
    /// `mantle_limb_time` sets the activation time for the Mantle Limb network upgrade.
    /// Active if `mantle_limb_time` != None && L2 block timestamp >= `Some(mantle_limb_time)`,
    /// inactive otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_limb_time: Option<u64>,
    /// `mantle_arsia_time` sets the activation time for the Mantle Arsia network upgrade.
    /// Active if `mantle_arsia_time` != None && L2 block timestamp >= `Some(mantle_arsia_time)`,
    /// inactive otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_arsia_time: Option<u64>,
    /// `mantle_elysium_time` sets the activation time for the Mantle Elysium network upgrade.
    /// Active if `mantle_elysium_time` != None && L2 block timestamp >=
    /// `Some(mantle_elysium_time)`, inactive otherwise.
    ///
    /// Elysium ends the Arsia-era pin on L1's blob-fee schedule: from Arsia until Elysium,
    /// Mantle mainnet computes the L1-info `BlobBaseFee` with the Prague schedule regardless of
    /// what L1 has activated; from Elysium on, L1's real Osaka/BPO schedule applies. See
    /// `L1BlockInfoTx::try_new`.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_elysium_time: Option<u64>,
}

impl MantleHardForkConfig {
    /// No Mantle hardfork scheduled — the correct value for a non-Mantle chain.
    ///
    /// [`Default`] cannot be used from a `const` initializer, and several `const RollupConfig`
    /// definitions for OP chains need this field. Keeping it here means a newly added Mantle
    /// hardfork only has to be defaulted in one place.
    pub const NONE: Self = Self {
        mantle_base_fee_time: None,
        mantle_everest_time: None,
        mantle_euboea_time: None,
        mantle_skadi_time: None,
        mantle_limb_time: None,
        mantle_arsia_time: None,
        mantle_elysium_time: None,
    };

    /// Returns an iterator of Mantle hardfork names -> their activation times (if scheduled.)
    pub fn iter(&self) -> impl Iterator<Item = (&'static str, Option<u64>)> {
        self.ordered().into_iter()
    }

    /// `[MANTLE]` The JSON keys this build understands, in activation order.
    ///
    /// **`deny_unknown_fields` on this struct does not protect the real path.** `RollupConfig`
    /// embeds it with `#[serde(flatten)]`, and serde documents `deny_unknown_fields` as
    /// unsupported in combination with `flatten` — it is silently inert there. Deserialising a
    /// `RollupConfig` therefore accepts and *drops* any `mantle_*_time` key this build does not
    /// know, which is precisely the "op-node schedules a fork, kona ignores it" consensus
    /// divergence the attribute appears to prevent. Verified, not assumed: see
    /// `flatten_defeats_deny_unknown_fields_on_the_real_path`.
    ///
    /// Callers that deserialise a config from an untrusted source must scan its raw keys with
    /// [`Self::looks_like_time_key`] / [`Self::is_known_time_key`] and reject the unknown ones.
    /// The host does this in `read_rollup_config` / `read_rollup_configs`.
    ///
    /// Keep in sync with the private `ordered()` list when adding a hardfork.
    pub const TIME_KEYS: [&'static str; 7] = [
        "mantle_base_fee_time",
        "mantle_everest_time",
        "mantle_euboea_time",
        "mantle_skadi_time",
        "mantle_limb_time",
        "mantle_arsia_time",
        "mantle_elysium_time",
    ];

    /// Whether `key` names a Mantle hardfork activation time this build implements.
    pub fn is_known_time_key(key: &str) -> bool {
        Self::TIME_KEYS.contains(&key)
    }

    /// Whether `key` looks like a Mantle hardfork activation time, implemented or not.
    ///
    /// Every Mantle fork field in op-node's `rollup.Config` is tagged `mantle_<name>_time`
    /// (`op-node/rollup/types.go:138-162`), so this shape is the net. A future fork that breaks
    /// the convention would slip through — which is what [`Self::TIME_KEYS`] being checked
    /// against `ordered()` in tests is for.
    pub fn looks_like_time_key(key: &str) -> bool {
        key.starts_with("mantle_") && key.ends_with("_time")
    }

    /// `[MANTLE]` Validates that the scheduled Mantle hardforks are ordered and contiguous.
    ///
    /// Port of `op-node/rollup/mantle_types.go::CheckMantleForks`, which op-node runs at startup
    /// (via `AlignOpWithMantle`, called from `op-node/cmd/main.go`). Two ways a config can be
    /// wrong, both of which op-node rejects and kona used to accept silently:
    ///
    /// * a fork is scheduled while an earlier one is not — e.g. Elysium set but Arsia missing;
    /// * a fork is scheduled *before* an earlier one.
    ///
    /// A trailing run of unscheduled forks is fine: that is just "we have not got there yet".
    ///
    /// Extend the private `ordered()` list when adding a hardfork — the check walks it pairwise, so
    /// a new fork is covered by adding it in one place.
    pub fn check_fork_order(&self) -> Result<(), MantleForkOrderError> {
        let forks = self.ordered();
        for window in forks.windows(2) {
            let [(prev_name, prev), (name, current)] = window else { continue };
            match (prev, current) {
                (None, Some(t)) => {
                    return Err(MantleForkOrderError::MissingPriorFork {
                        fork: name,
                        activation: *t,
                        prior: prev_name,
                    });
                }
                (Some(p), Some(c)) if p > c => {
                    return Err(MantleForkOrderError::OutOfOrder {
                        fork: name,
                        activation: *c,
                        prior: prev_name,
                        prior_activation: *p,
                    });
                }
                _ => {}
            }
        }
        Ok(())
    }

    /// The Mantle hardforks in activation order, paired with their configured times.
    const fn ordered(&self) -> [(&'static str, Option<u64>); 7] {
        [
            ("Mantle BaseFee", self.mantle_base_fee_time),
            ("Mantle Everest", self.mantle_everest_time),
            ("Mantle Euboea", self.mantle_euboea_time),
            ("Mantle Skadi", self.mantle_skadi_time),
            ("Mantle Limb", self.mantle_limb_time),
            ("Mantle Arsia", self.mantle_arsia_time),
            ("Mantle Elysium", self.mantle_elysium_time),
        ]
    }

    /// Returns true if any Mantle hardfork is configured (not None).
    ///
    /// This is used to determine if this is a Mantle chain or a chain that uses Mantle hardforks.
    /// This approach is more flexible than checking `chain_id`, as it works for testnets and
    /// custom deployments.
    pub const fn has_any_hardfork(&self) -> bool {
        self.mantle_base_fee_time.is_some() ||
            self.mantle_everest_time.is_some() ||
            self.mantle_euboea_time.is_some() ||
            self.mantle_skadi_time.is_some() ||
            self.mantle_limb_time.is_some() ||
            self.mantle_arsia_time.is_some() ||
            self.mantle_elysium_time.is_some()
    }
}

#[cfg(test)]
#[cfg(feature = "serde")]
mod tests {
    use super::*;

    #[test]
    fn test_mantle_hardforks_deserialize_json() {
        let raw: &str = r#"
        {
            "mantle_base_fee_time": 1000,
            "mantle_arsia_time": 2000
        }
        "#;

        let hardforks = MantleHardForkConfig {
            mantle_base_fee_time: Some(1000),
            mantle_everest_time: None,
            mantle_euboea_time: None,
            mantle_skadi_time: None,
            mantle_limb_time: None,
            mantle_arsia_time: Some(2000),
            mantle_elysium_time: None,
        };

        let deserialized: MantleHardForkConfig = serde_json::from_str(raw).unwrap();
        assert_eq!(hardforks, deserialized);
    }

    #[test]
    fn test_mantle_hardforks_iter() {
        let hardforks = MantleHardForkConfig {
            mantle_base_fee_time: Some(12),
            mantle_everest_time: Some(13),
            mantle_euboea_time: Some(14),
            mantle_skadi_time: Some(15),
            mantle_limb_time: Some(16),
            mantle_arsia_time: Some(17),
            mantle_elysium_time: Some(18),
        };

        let mut iter = hardforks.iter();
        assert_eq!(iter.next(), Some(("Mantle BaseFee", Some(12))));
        assert_eq!(iter.next(), Some(("Mantle Everest", Some(13))));
        assert_eq!(iter.next(), Some(("Mantle Euboea", Some(14))));
        assert_eq!(iter.next(), Some(("Mantle Skadi", Some(15))));
        assert_eq!(iter.next(), Some(("Mantle Limb", Some(16))));
        assert_eq!(iter.next(), Some(("Mantle Arsia", Some(17))));
        assert_eq!(iter.next(), Some(("Mantle Elysium", Some(18))));
        assert_eq!(iter.next(), None);
    }

    #[test]
    fn test_has_any_hardfork() {
        // Test with all hardforks configured
        let all_hardforks = MantleHardForkConfig {
            mantle_base_fee_time: Some(12),
            mantle_everest_time: Some(13),
            mantle_euboea_time: Some(14),
            mantle_skadi_time: Some(15),
            mantle_limb_time: Some(16),
            mantle_arsia_time: Some(17),
            mantle_elysium_time: Some(18),
        };
        assert!(all_hardforks.has_any_hardfork());

        // Test with only one hardfork configured
        let one_hardfork =
            MantleHardForkConfig { mantle_limb_time: Some(100), ..Default::default() };
        assert!(one_hardfork.has_any_hardfork());

        // Test with only arsia configured
        let arsia_only =
            MantleHardForkConfig { mantle_arsia_time: Some(200), ..Default::default() };
        assert!(arsia_only.has_any_hardfork());

        // Test with no hardforks configured (default)
        let no_hardforks = MantleHardForkConfig::default();
        assert!(!no_hardforks.has_any_hardfork());

        // Test with all None
        let all_none = MantleHardForkConfig {
            mantle_base_fee_time: None,
            mantle_everest_time: None,
            mantle_euboea_time: None,
            mantle_skadi_time: None,
            mantle_limb_time: None,
            mantle_arsia_time: None,
            mantle_elysium_time: None,
        };
        assert!(!all_none.has_any_hardfork());
    }

    /// `[MANTLE]` Elysium parses. Unknown forks are rejected **only when this struct is
    /// deserialised directly** — which nothing in production does.
    #[test]
    fn elysium_parses_and_direct_deserialization_rejects_unknown_forks() {
        let cfg: MantleHardForkConfig = serde_json::from_str(
            r#"{ "mantle_skadi_time": 10, "mantle_arsia_time": 20, "mantle_elysium_time": 30 }"#,
        )
        .expect("Elysium is implemented and must deserialize");
        assert_eq!(cfg.mantle_elysium_time, Some(30));

        let err = serde_json::from_str::<MantleHardForkConfig>(
            r#"{ "mantle_arsia_time": 20, "mantle_someday_time": 40 }"#,
        )
        .expect_err("direct deserialization honours deny_unknown_fields");
        assert!(err.to_string().contains("mantle_someday_time"), "got: {err}");
    }

    /// `[MANTLE]` The above guarantee **does not survive `#[serde(flatten)]`**, and this test
    /// exists to stop anyone (including a future me) from believing it does.
    ///
    /// `RollupConfig` embeds this struct with `flatten`; serde documents `deny_unknown_fields`
    /// as unsupported in that combination, and it is silently inert rather than a compile error.
    /// So on the only path production uses, an unimplemented `mantle_*_time` is accepted and
    /// dropped — op-node would activate that fork and kona would not, with nothing logged.
    ///
    /// The real guard is a raw-key scan at the config load points
    /// (`kona_host::mantle_config::unknown_mantle_forks`, driven by [`TIME_KEYS`]). If serde
    /// ever fixes this and the assertion below starts failing, that scan becomes redundant
    /// belt-and-braces rather than the only thing standing there — check before deleting it.
    ///
    /// [`TIME_KEYS`]: MantleHardForkConfig::TIME_KEYS
    #[test]
    fn flatten_defeats_deny_unknown_fields_on_the_real_path() {
        use crate::RollupConfig;

        let base = RollupConfig {
            mantle_hardforks: MantleHardForkConfig {
                mantle_skadi_time: Some(10),
                mantle_arsia_time: Some(20),
                ..MantleHardForkConfig::NONE
            },
            ..Default::default()
        };
        let mut raw = serde_json::to_value(&base).unwrap();

        // Premise: the fields really are flattened to the top level, as op-node writes them
        // (`op-node/rollup/types.go:138-162`).
        assert!(raw.get("mantle_skadi_time").is_some(), "fields must be flattened, not nested");

        raw.as_object_mut().unwrap().insert("mantle_someday_time".into(), serde_json::json!(40));
        let parsed: RollupConfig = serde_json::from_value(raw.clone())
            .expect("flatten swallows the unknown key instead of rejecting it");
        assert_eq!(parsed.mantle_hardforks.mantle_skadi_time, Some(10));

        // And the scan that actually catches it agrees this key is unknown.
        let key = "mantle_someday_time";
        assert!(MantleHardForkConfig::looks_like_time_key(key));
        assert!(!MantleHardForkConfig::is_known_time_key(key));
    }

    /// `TIME_KEYS` is what the load-point scan trusts; `ordered()` is what the fork-order check
    /// and `iter()` walk. They must not drift.
    #[test]
    fn time_keys_and_ordered_stay_in_step() {
        let all = MantleHardForkConfig {
            mantle_base_fee_time: Some(1),
            mantle_everest_time: Some(2),
            mantle_euboea_time: Some(3),
            mantle_skadi_time: Some(4),
            mantle_limb_time: Some(5),
            mantle_arsia_time: Some(6),
            mantle_elysium_time: Some(7),
        };
        assert_eq!(MantleHardForkConfig::TIME_KEYS.len(), all.iter().count());

        // Every key must appear in the serialized form of a fully-scheduled config, which is
        // what ties the const to serde's actual field names rather than to a hand-kept list.
        let raw = serde_json::to_value(all).unwrap();
        for key in MantleHardForkConfig::TIME_KEYS {
            assert!(raw.get(key).is_some(), "`{key}` is in TIME_KEYS but is not a serde field");
        }
    }

    /// `[MANTLE]` Port of op-node's `CheckMantleForks` (`rollup/mantle_types.go`).
    ///
    /// Both failure modes matter now that Elysium is schedulable: setting Elysium without Arsia,
    /// or before it, is a config op-node rejects at startup and kona used to accept silently.
    #[test]
    fn check_fork_order_matches_op_node() {
        let full = MantleHardForkConfig {
            mantle_base_fee_time: Some(1),
            mantle_everest_time: Some(2),
            mantle_euboea_time: Some(3),
            mantle_skadi_time: Some(4),
            mantle_limb_time: Some(5),
            mantle_arsia_time: Some(6),
            mantle_elysium_time: Some(7),
        };
        assert!(full.check_fork_order().is_ok());

        // Equal timestamps are allowed — op-node only rejects `*a > *b`.
        let simultaneous = MantleHardForkConfig { mantle_elysium_time: Some(6), ..full };
        assert!(simultaneous.check_fork_order().is_ok());

        // A trailing run of unscheduled forks is "we have not got there yet".
        let no_elysium = MantleHardForkConfig { mantle_elysium_time: None, ..full };
        assert!(no_elysium.check_fork_order().is_ok());
        assert!(MantleHardForkConfig::NONE.check_fork_order().is_ok());

        // Elysium scheduled, Arsia missing.
        let gap = MantleHardForkConfig { mantle_arsia_time: None, ..full };
        assert_eq!(
            gap.check_fork_order(),
            Err(MantleForkOrderError::MissingPriorFork {
                fork: "Mantle Elysium",
                activation: 7,
                prior: "Mantle Arsia",
            }),
        );

        // Elysium before Arsia.
        let backwards = MantleHardForkConfig { mantle_elysium_time: Some(5), ..full };
        assert_eq!(
            backwards.check_fork_order(),
            Err(MantleForkOrderError::OutOfOrder {
                fork: "Mantle Elysium",
                activation: 5,
                prior: "Mantle Arsia",
                prior_activation: 6,
            }),
        );

        // The check is not Elysium-specific: a hole anywhere is caught.
        let early_gap = MantleHardForkConfig { mantle_everest_time: None, ..full };
        assert!(matches!(
            early_gap.check_fork_order(),
            Err(MantleForkOrderError::MissingPriorFork { fork: "Mantle Euboea", .. }),
        ));
    }

    /// `iter()` and `check_fork_order()` must walk the same list, or a newly added fork gets
    /// validated but not reported (or the reverse).
    #[test]
    fn every_configured_fork_is_covered_by_the_order_check() {
        let all = MantleHardForkConfig {
            mantle_base_fee_time: Some(1),
            mantle_everest_time: Some(2),
            mantle_euboea_time: Some(3),
            mantle_skadi_time: Some(4),
            mantle_limb_time: Some(5),
            mantle_arsia_time: Some(6),
            mantle_elysium_time: Some(7),
        };
        // Every field is Some, so every entry the check walks must be Some too. If a field is
        // added to the struct but not to `ordered()`, this count stops matching.
        assert_eq!(all.iter().filter(|(_, t)| t.is_some()).count(), 7);
        assert!(all.has_any_hardfork());
    }
}
