//! Contains the Mantle-specific hardfork configuration for the chain.

/// Mantle-specific hardfork configuration.
#[derive(Debug, Copy, Clone, Default, Hash, Eq, PartialEq)]
#[cfg_attr(feature = "arbitrary", derive(arbitrary::Arbitrary))]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
#[cfg_attr(feature = "serde", serde(deny_unknown_fields))]
pub struct MantleHardForkConfig {
    /// `mantle_base_fee_time` sets the activation time for the Mantle BaseFee network upgrade.
    /// Active if `mantle_base_fee_time` != None && L2 block timestamp >= Some(mantle_base_fee_time), inactive
    /// otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_base_fee_time: Option<u64>,
    /// `mantle_everest_time` sets the activation time for the Mantle Everest network upgrade.
    /// Active if `mantle_everest_time` != None && L2 block timestamp >= Some(mantle_everest_time), inactive
    /// otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_everest_time: Option<u64>,
    /// `mantle_euboea_time` sets the activation time for the Mantle Euboea network upgrade.
    /// Active if `mantle_euboea_time` != None && L2 block timestamp >= Some(mantle_euboea_time), inactive
    /// otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_euboea_time: Option<u64>,
    /// `mantle_skadi_time` sets the activation time for the Mantle Skadi network upgrade.
    /// Active if `mantle_skadi_time` != None && L2 block timestamp >= Some(mantle_skadi_time), inactive
    /// otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_skadi_time: Option<u64>,
    /// `mantle_limb_time` sets the activation time for the Mantle Limb network upgrade.
    /// Active if `mantle_limb_time` != None && L2 block timestamp >= Some(mantle_limb_time), inactive
    /// otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_limb_time: Option<u64>,
    /// `mantle_arsia_time` sets the activation time for the Mantle Arsia network upgrade.
    /// Active if `mantle_arsia_time` != None && L2 block timestamp >= Some(mantle_arsia_time), inactive
    /// otherwise.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub mantle_arsia_time: Option<u64>,
}

impl MantleHardForkConfig {
    /// Returns an iterator of Mantle hardfork names -> their activation times (if scheduled.)
    pub fn iter(&self) -> impl Iterator<Item = (&'static str, Option<u64>)> {
        [
            ("Mantle BaseFee", self.mantle_base_fee_time),
            ("Mantle Everest", self.mantle_everest_time),
            ("Mantle Euboea", self.mantle_euboea_time),
            ("Mantle Skadi", self.mantle_skadi_time),
            ("Mantle Limb", self.mantle_limb_time),
            ("Mantle Arsia", self.mantle_arsia_time),
        ]
        .into_iter()
    }

    /// Returns true if any Mantle hardfork is configured (not None).
    ///
    /// This is used to determine if this is a Mantle chain or a chain that uses Mantle hardforks.
    /// This approach is more flexible than checking chain_id, as it works for testnets and
    /// custom deployments.
    pub const fn has_any_hardfork(&self) -> bool {
        self.mantle_base_fee_time.is_some()
            || self.mantle_everest_time.is_some()
            || self.mantle_euboea_time.is_some()
            || self.mantle_skadi_time.is_some()
            || self.mantle_limb_time.is_some()
            || self.mantle_arsia_time.is_some()
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
        };

        let mut iter = hardforks.iter();
        assert_eq!(iter.next(), Some(("Mantle BaseFee", Some(12))));
        assert_eq!(iter.next(), Some(("Mantle Everest", Some(13))));
        assert_eq!(iter.next(), Some(("Mantle Euboea", Some(14))));
        assert_eq!(iter.next(), Some(("Mantle Skadi", Some(15))));
        assert_eq!(iter.next(), Some(("Mantle Limb", Some(16))));
        assert_eq!(iter.next(), Some(("Mantle Arsia", Some(17))));
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
        };
        assert!(all_hardforks.has_any_hardfork());

        // Test with only one hardfork configured
        let one_hardfork = MantleHardForkConfig {
            mantle_limb_time: Some(100),
            ..Default::default()
        };
        assert!(one_hardfork.has_any_hardfork());

        // Test with only arsia configured
        let arsia_only = MantleHardForkConfig {
            mantle_arsia_time: Some(200),
            ..Default::default()
        };
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
        };
        assert!(!all_none.has_any_hardfork());
    }
}
