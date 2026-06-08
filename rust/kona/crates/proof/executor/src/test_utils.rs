//! Test utilities for the executor.

use crate::{BlockBuildingOutcome, ExecutorResult, StatelessL2Builder, TrieDBProvider};
// [MANTLE] Imports used by the Mantle additions in this module
// (Chain helpers for chain-id construction, URL parsing for the HTTP fetcher).
use alloy_chains::Chain;
use reqwest::Url;
use alloy_consensus::Header;
use alloy_op_evm::OpEvmFactory;
use alloy_primitives::{B256, Bytes, Sealable};
use alloy_provider::{Provider, RootProvider, network::primitives::BlockTransactions};
use alloy_rlp::Decodable;
use alloy_rpc_client::RpcClient;
use alloy_rpc_types_engine::PayloadAttributes;
use alloy_transport_http::{Client, Http};
use kona_genesis::RollupConfig;
use kona_mpt::{NoopTrieHinter, TrieNode, TrieProvider};
use kona_registry::ROLLUP_CONFIGS;
use op_alloy_rpc_types_engine::OpPayloadAttributes;
use rocksdb::{DB, Options};
use serde::{Deserialize, Serialize};
use std::{
    path::{Path, PathBuf},
    sync::Arc,
};
use tokio::{fs, runtime::Handle, sync::Mutex};

/// A loaded executor test fixture and its backing temporary fixture directory.
#[derive(Debug)]
pub struct LoadedExecutorTestFixture {
    /// Keeps the untarred fixture directory alive while the `RocksDB` provider is open.
    pub fixture_dir: tempfile::TempDir,
    /// The deserialized fixture metadata and payload.
    pub fixture: ExecutorTestFixture,
    /// Trie/database provider backed by the fixture's key-value store.
    pub provider: DiskTrieNodeProvider,
}

/// Loads a [`ExecutorTestFixture`] stored at the passed `fixture_path`.
pub async fn load_test_fixture(fixture_path: PathBuf) -> LoadedExecutorTestFixture {
    let fixture_dir = tempfile::tempdir().expect("Failed to create temporary directory");
    tokio::process::Command::new("tar")
        .arg("-xvf")
        .arg(fixture_path.as_path())
        .arg("-C")
        .arg(fixture_dir.path())
        .arg("--strip-components=1")
        .output()
        .await
        .expect("Failed to untar fixture");

    let mut options = Options::default();
    options.set_compression_type(rocksdb::DBCompressionType::Snappy);
    options.create_if_missing(true);
    let kv_store = DB::open(&options, fixture_dir.path().join("kv"))
        .unwrap_or_else(|e| panic!("Failed to open database at {fixture_dir:?}: {e}"));
    let provider = DiskTrieNodeProvider::new(kv_store);
    let fixture: ExecutorTestFixture =
        serde_json::from_slice(&fs::read(fixture_dir.path().join("fixture.json")).await.unwrap())
            .expect("Failed to deserialize fixture");

    LoadedExecutorTestFixture { fixture_dir, fixture, provider }
}

/// Executes a loaded fixture, optionally overriding SDM activation for the run.
pub fn execute_loaded_fixture(
    loaded: LoadedExecutorTestFixture,
    sdm_active_override: Option<bool>,
) -> ExecutorResult<BlockBuildingOutcome> {
    let LoadedExecutorTestFixture { fixture_dir: _fixture_dir, fixture, provider } = loaded;
    let ExecutorTestFixture { rollup_config, parent_header, executing_payload, .. } = fixture;

    let mut executor = StatelessL2Builder::new(
        &rollup_config,
        OpEvmFactory::<alloy_op_evm::OpTx>::default(),
        provider,
        NoopTrieHinter,
        parent_header.seal_slow(),
    );
    executor.set_sdm_active_override(sdm_active_override);

    executor.build_block(executing_payload)
}

/// Executes a [`ExecutorTestFixture`] stored at the passed `fixture_path` and asserts that the
/// produced block hash matches the expected block hash.
pub async fn run_test_fixture(fixture_path: PathBuf) {
    let loaded = load_test_fixture(fixture_path).await;
    let expected_block_hash = loaded.fixture.expected_block_hash;
    let outcome = execute_loaded_fixture(loaded, None).unwrap();

    assert_eq!(
        outcome.header.hash(),
        expected_block_hash,
        "Produced header does not match the expected header"
    );
}

/// The test fixture format for the [`StatelessL2Builder`].
#[derive(Debug, Serialize, Deserialize)]
pub struct ExecutorTestFixture {
    /// The rollup configuration for the executing chain.
    pub rollup_config: RollupConfig,
    /// The parent block header.
    pub parent_header: Header,
    /// The executing payload attributes.
    pub executing_payload: OpPayloadAttributes,
    /// The expected block hash
    pub expected_block_hash: B256,
}

/// A test fixture creator for the [`StatelessL2Builder`].
#[derive(Debug)]
pub struct ExecutorTestFixtureCreator {
    /// The RPC provider for the L2 execution layer.
    pub provider: RootProvider,
    /// The block number to create the test fixture for.
    pub block_number: u64,
    /// The rollup config used to drive the executor for this fixture.
    pub rollup_config: RollupConfig,
    /// The key value store for the test fixture.
    pub kv_store: Arc<Mutex<rocksdb::DB>>,
    /// The data directory for the test fixture.
    pub data_dir: PathBuf,
    /// Whether to skip saving data (use temporary directory)
    pub skip_save: bool,
    /// Temporary directory (if skip_save is true)
    pub _temp_dir: Option<tempfile::TempDir>,
}

impl ExecutorTestFixtureCreator {
    /// Creates a new [`ExecutorTestFixtureCreator`] with the hardcoded
    /// [`mock_rollup_config`]. Prefer [`Self::new_with_options_and_config`]
    /// when the caller can supply a real `RollupConfig`.
    pub fn new(provider_url: &str, block_number: u64, base_fixture_directory: PathBuf) -> Self {
        Self::new_with_options(provider_url, block_number, base_fixture_directory, false)
    }

    /// Creates a new [`ExecutorTestFixtureCreator`] with skip_save option,
    /// using the hardcoded [`mock_rollup_config`].
    pub fn new_with_options(
        provider_url: &str,
        block_number: u64,
        base_fixture_directory: PathBuf,
        skip_save: bool,
    ) -> Self {
        Self::new_with_options_and_config(
            provider_url,
            block_number,
            base_fixture_directory,
            skip_save,
            mock_rollup_config(),
        )
    }

    /// Creates a new [`ExecutorTestFixtureCreator`] with an explicit
    /// [`RollupConfig`].
    ///
    /// If `skip_save` is true, data will be stored in a temporary directory
    /// and automatically cleaned up after execution.
    pub fn new_with_options_and_config(
        provider_url: &str,
        block_number: u64,
        base_fixture_directory: PathBuf,
        skip_save: bool,
        rollup_config: RollupConfig,
    ) -> Self {
        let url: Url = provider_url.parse().expect("Invalid provider URL");
        // Use reqwest::Client for HTTPS support
        let http = Http::<Client>::new(url);
        let provider = RootProvider::new(RpcClient::new(http, false));

        let mut options = Options::default();
        options.set_compression_type(rocksdb::DBCompressionType::Snappy);
        options.create_if_missing(true);

        let (data_dir, temp_dir, db) = if skip_save {
            // Use temporary directory
            let temp = tempfile::tempdir().expect("Failed to create temporary directory");
            let temp_path = temp.path().to_path_buf();
            let db = DB::open(&options, temp_path.join("kv").as_path())
                .unwrap_or_else(|e| panic!("Failed to open temporary database: {e}"));
            (temp_path, Some(temp), db)
        } else {
            // Use provided directory
            let base = base_fixture_directory.join(format!("block-{block_number}"));
            let db = DB::open(&options, base.join("kv").as_path())
                .unwrap_or_else(|e| panic!("Failed to open database at {base:?}: {e}"));
            (base, None, db)
        };

        Self {
            provider,
            block_number,
            rollup_config,
            kv_store: Arc::new(Mutex::new(db)),
            data_dir,
            skip_save,
            _temp_dir: temp_dir,
        }
    }
}

// [MANTLE] Hardcoded fallback used by the back-compat constructors and the
// in-crate executor unit tests. Real entry points should load a `RollupConfig`
// from a JSON file via [`load_rollup_config_from_file`] and pass it through
// [`ExecutorTestFixtureCreator::new_with_options_and_config`].
fn mock_rollup_config() -> RollupConfig {
    let mut rollup_config =
        RollupConfig { l2_chain_id: Chain::from_id(1115511107), ..Default::default() };
    rollup_config.mantle_hardforks.mantle_skadi_time = Some(0);
    // rollup_config.mantle_hardforks.mantle_limb_time = Some(0);
    rollup_config.hardforks.jovian_time = Some(1768212000);
    rollup_config.hardforks.holocene_time = Some(1768212000);
    rollup_config.mantle_hardforks.mantle_arsia_time = Some(1768212000);
    rollup_config
}

/// Errors returned by [`load_rollup_config_from_file`].
#[derive(Debug, thiserror::Error)]
pub enum ConfigLoadError {
    /// Filesystem I/O failure while reading the rollup config file.
    #[error(transparent)]
    Io(#[from] std::io::Error),
    /// JSON decode failure.
    #[error(transparent)]
    Json(#[from] serde_json::Error),
}

/// Loads a [`RollupConfig`] from a JSON file produced by op-node's
/// `optimism_rollupConfig` (or any compatible serialization — both
/// `hardforks` and `mantle_hardforks` are `#[serde(flatten)]` so the on-wire
/// shape is flat).
pub fn load_rollup_config_from_file(path: &Path) -> Result<RollupConfig, ConfigLoadError> {
    let bytes = std::fs::read(path)?;
    Ok(serde_json::from_slice(&bytes)?)
}

impl ExecutorTestFixtureCreator {
    /// Create a static test fixture with the configuration provided.
    pub async fn create_static_fixture(self) -> Result<bool, TestTrieNodeProviderError> {
        // [MANTLE] Was `let rollup_config = mock_rollup_config();` — use the
        // RollupConfig threaded in through `new_with_options_and_config` so
        // the caller can supply real per-chain timestamps via JSON.
        let rollup_config = self.rollup_config.clone();

        let executing_block =
            match self.provider.get_block_by_number(self.block_number.into()).await {
                Ok(Some(block)) => block,
                Ok(None) => {
                    warn!(
                        target: "kona_executor::test_utils",
                        block_number = self.block_number,
                        "Block not found"
                    );
                    return Err(TestTrieNodeProviderError::PreimageNotFound);
                }
                Err(e) => {
                    warn!(
                        target: "kona_executor::test_utils",
                        block_number = self.block_number,
                        error = ?e,
                        "Failed to get executing block"
                    );
                    return Err(TestTrieNodeProviderError::PreimageNotFound);
                }
            };

        let parent_block =
            match self.provider.get_block_by_number((self.block_number - 1).into()).await {
                Ok(Some(block)) => block,
                Ok(None) => {
                    warn!(
                        target: "kona_executor::test_utils",
                        block_number = self.block_number - 1,
                        "Parent block not found"
                    );
                    return Err(TestTrieNodeProviderError::PreimageNotFound);
                }
                Err(e) => {
                    warn!(
                        target: "kona_executor::test_utils",
                        block_number = self.block_number - 1,
                        error = ?e,
                        "Failed to get parent block"
                    );
                    return Err(TestTrieNodeProviderError::PreimageNotFound);
                }
            };

        let executing_header = executing_block.header;
        let parent_header = parent_block.header.inner.seal_slow();

        let encoded_executing_transactions = match executing_block.transactions {
            BlockTransactions::Hashes(transactions) => {
                let mut encoded_transactions = Vec::with_capacity(transactions.len());
                info!(
                    target: "kona_executor::test_utils",
                    tx_count = transactions.len(),
                    "Processing transactions"
                );

                for (i, tx_hash) in transactions.iter().enumerate() {
                    match self
                        .provider
                        .client()
                        .request::<&[B256; 1], Bytes>("debug_getRawTransaction", &[*tx_hash])
                        .await
                    {
                        Ok(tx) => {
                            encoded_transactions.push(tx);
                        }
                        Err(e) => {
                            warn!(
                                target: "kona_executor::test_utils",
                                tx_index = i,
                                tx_hash = ?tx_hash,
                                error = ?e,
                                "Failed to get raw transaction"
                            );
                            return Err(TestTrieNodeProviderError::PreimageNotFound);
                        }
                    }
                }
                encoded_transactions
            }
            _ => {
                warn!(
                    target: "kona_executor::test_utils",
                    "Only BlockTransactions::Hashes are supported"
                );
                panic!("Only BlockTransactions::Hashes are supported.");
            }
        };

        let payload_attrs = OpPayloadAttributes {
            payload_attributes: PayloadAttributes {
                timestamp: executing_header.timestamp,
                parent_beacon_block_root: executing_header.parent_beacon_block_root,
                prev_randao: executing_header.mix_hash,
                withdrawals: Default::default(),
                suggested_fee_recipient: executing_header.beneficiary,
                slot_number: Default::default(),
            },
            gas_limit: Some(executing_header.gas_limit),
            transactions: Some(encoded_executing_transactions),
            no_tx_pool: Some(true),
            eip_1559_params: rollup_config.is_holocene_active(executing_header.timestamp).then(
                || {
                    executing_header.extra_data[1..9]
                        .try_into()
                        .expect("Invalid header format for Holocene")
                },
            ),
            min_base_fee: rollup_config.is_jovian_active(executing_header.timestamp).then(|| {
                // The min base fee is the bytes 9-17 of the extra data.
                executing_header.extra_data[9..17]
                    .try_into()
                    .map(u64::from_be_bytes)
                    .expect("Invalid header format for Jovian")
            }),
        };

        let fixture_path = self.data_dir.join("fixture.json");
        let fixture = ExecutorTestFixture {
            rollup_config: rollup_config.clone(),
            parent_header: parent_header.inner().clone(),
            executing_payload: payload_attrs.clone(),
            expected_block_hash: executing_header.hash_slow(),
        };

        let mut executor = StatelessL2Builder::new(
            &rollup_config,
            OpEvmFactory::<alloy_op_evm::OpTx>::default(),
            self,
            NoopTrieHinter,
            parent_header,
        );
        let outcome = executor.build_block(payload_attrs).expect("Failed to execute block");

        assert_eq!(
            outcome.header.inner(),
            &executing_header.inner,
            "Produced header does not match the expected header"
        );
        fs::write(fixture_path.as_path(), serde_json::to_vec(&fixture).unwrap()).await.unwrap();

        // Tar the fixture.
        let data_dir = fixture_path.parent().unwrap();
        tokio::process::Command::new("tar")
            .arg("-czf")
            .arg(data_dir.with_extension("tar.gz").file_name().unwrap())
            .arg(data_dir.file_name().unwrap())
            .current_dir(data_dir.parent().unwrap())
            .output()
            .await
            .expect("Failed to tar fixture");

        // Remove the leftover directory.
        fs::remove_dir_all(data_dir).await.expect("Failed to remove temporary directory");

        // [MANTLE] create_static_fixture returns Result<bool, _> in the Mantle test_utils;
        // emit a placeholder success value until a richer status is wired in.
        Ok(true)
    }
}

impl TrieProvider for ExecutorTestFixtureCreator {
    type Error = TestTrieNodeProviderError;

    fn trie_node_by_hash(&self, key: B256) -> Result<TrieNode, Self::Error> {
        // Fetch the preimage from the L2 chain provider.
        let preimage: Bytes = tokio::task::block_in_place(move || {
            Handle::current().block_on(async {
                let preimage_result: Result<Bytes, _> =
                    self.provider.client().request("debug_dbGet", &[key]).await;

                let preimage = match preimage_result {
                    Ok(data) => data,
                    Err(e) => {
                        warn!(
                            target: "kona_executor::test_utils",
                            key = ?key,
                            error = ?e,
                            "Failed to get trie node preimage from debug_dbGet"
                        );
                        return Err(TestTrieNodeProviderError::PreimageNotFound);
                    }
                };

                // Store the preimage in the KV store for caching
                if let Err(e) = self.kv_store.lock().await.put(key, preimage.clone()) {
                    warn!(
                        target: "kona_executor::test_utils",
                        key = ?key,
                        error = ?e,
                        "Failed to store preimage in KV store"
                    );
                    return Err(TestTrieNodeProviderError::KVStore);
                }

                Ok(preimage)
            })
        })?;

        // Decode the preimage into a trie node.
        TrieNode::decode(&mut preimage.as_ref()).map_err(|e| {
            warn!(
                target: "kona_executor::test_utils",
                key = ?key,
                error = ?e,
                "Failed to decode trie node from preimage"
            );
            TestTrieNodeProviderError::Rlp(e)
        })
    }
}

impl TrieDBProvider for ExecutorTestFixtureCreator {
    fn bytecode_by_hash(&self, hash: B256) -> Result<Bytes, Self::Error> {
        // geth hashdb scheme code hash key prefix
        const CODE_PREFIX: u8 = b'c';

        // Fetch the preimage from the L2 chain provider.
        let preimage: Bytes = tokio::task::block_in_place(move || {
            Handle::current().block_on(async {
                // Attempt to fetch the code from the L2 chain provider.
                let code_hash = [&[CODE_PREFIX], hash.as_slice()].concat();
                let code_result = self
                    .provider
                    .client()
                    .request::<&[Bytes; 1], Bytes>("debug_dbGet", &[code_hash.into()])
                    .await;

                // Check if the first attempt to fetch the code failed. If it did, try fetching the
                // code hash preimage without the geth hashdb scheme prefix.
                let code = match code_result {
                    Ok(code) => code,
                    Err(e) => {
                        warn!(
                            target: "kona_executor::test_utils",
                            hash = ?hash,
                            error = ?e,
                            "Failed to get bytecode with prefix, trying without prefix"
                        );

                        match self
                            .provider
                            .client()
                            .request::<&[B256; 1], Bytes>("debug_dbGet", &[hash])
                            .await
                        {
                            Ok(code) => code,
                            Err(e2) => {
                                warn!(
                                    target: "kona_executor::test_utils",
                                    hash = ?hash,
                                    error = ?e2,
                                    "Failed to get bytecode without prefix"
                                );
                                return Err(TestTrieNodeProviderError::PreimageNotFound);
                            }
                        }
                    }
                };

                // Store the bytecode in the KV store for caching
                if let Err(e) = self.kv_store.lock().await.put(hash, code.clone()) {
                    warn!(
                        target: "kona_executor::test_utils",
                        hash = ?hash,
                        error = ?e,
                        "Failed to store bytecode in KV store"
                    );
                    return Err(TestTrieNodeProviderError::KVStore);
                }

                Ok(code)
            })
        })?;

        Ok(preimage)
    }

    fn header_by_hash(&self, hash: B256) -> Result<Header, Self::Error> {
        let encoded_header: Bytes = tokio::task::block_in_place(move || {
            Handle::current().block_on(async {
                let header_result: Result<Bytes, _> =
                    self.provider.client().request("debug_getRawHeader", &[hash]).await;

                let preimage = match header_result {
                    Ok(data) => data,
                    Err(e) => {
                        warn!(
                            target: "kona_executor::test_utils",
                            hash = ?hash,
                            error = ?e,
                            "Failed to get header preimage from debug_getRawHeader"
                        );
                        return Err(TestTrieNodeProviderError::PreimageNotFound);
                    }
                };

                // Store the header in the KV store for caching
                if let Err(e) = self.kv_store.lock().await.put(hash, preimage.clone()) {
                    warn!(
                        target: "kona_executor::test_utils",
                        hash = ?hash,
                        error = ?e,
                        "Failed to store header in KV store"
                    );
                    return Err(TestTrieNodeProviderError::KVStore);
                }

                Ok(preimage)
            })
        })?;

        // Decode the Header.
        Header::decode(&mut encoded_header.as_ref()).map_err(|e| {
            warn!(
                target: "kona_executor::test_utils",
                hash = ?hash,
                error = ?e,
                "Failed to decode header from preimage"
            );
            TestTrieNodeProviderError::Rlp(e)
        })
    }
}

/// A simple [`TrieDBProvider`] that reads data from a disk-based key-value store.
#[derive(Debug)]
pub struct DiskTrieNodeProvider {
    kv_store: DB,
}

impl DiskTrieNodeProvider {
    /// Creates a new [`DiskTrieNodeProvider`] with the given [`rocksdb`] K/V store.
    pub const fn new(kv_store: DB) -> Self {
        Self { kv_store }
    }
}

impl TrieProvider for DiskTrieNodeProvider {
    type Error = TestTrieNodeProviderError;

    fn trie_node_by_hash(&self, key: B256) -> Result<TrieNode, Self::Error> {
        TrieNode::decode(
            &mut self
                .kv_store
                .get(key)
                .map_err(|_| TestTrieNodeProviderError::PreimageNotFound)?
                .ok_or(TestTrieNodeProviderError::PreimageNotFound)?
                .as_slice(),
        )
        .map_err(TestTrieNodeProviderError::Rlp)
    }
}

impl TrieDBProvider for DiskTrieNodeProvider {
    fn bytecode_by_hash(&self, code_hash: B256) -> Result<Bytes, Self::Error> {
        self.kv_store
            .get(code_hash)
            .map_err(|_| TestTrieNodeProviderError::PreimageNotFound)?
            .map(Bytes::from)
            .ok_or(TestTrieNodeProviderError::PreimageNotFound)
    }

    fn header_by_hash(&self, hash: B256) -> Result<Header, Self::Error> {
        Header::decode(
            &mut self
                .kv_store
                .get(hash)
                .map_err(|_| TestTrieNodeProviderError::PreimageNotFound)?
                .ok_or(TestTrieNodeProviderError::PreimageNotFound)?
                .as_slice(),
        )
        .map_err(TestTrieNodeProviderError::Rlp)
    }
}

/// An error type for the [`DiskTrieNodeProvider`] and [`ExecutorTestFixtureCreator`].
#[derive(Debug, thiserror::Error)]
pub enum TestTrieNodeProviderError {
    /// The preimage was not found in the key-value store.
    #[error("Preimage not found")]
    PreimageNotFound,
    /// Failed to decode the RLP-encoded data.
    #[error("Failed to decode RLP: {0}")]
    Rlp(alloy_rlp::Error),
    /// Failed to write back to the key-value store.
    #[error("Failed to write back to key value store")]
    KVStore,
    /// Failed to execute the block
    #[error("Failed to execute the block")]
    ExecutionFailed,
}
