// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

interface IL2CrossDomainMessenger {
    function L1_MNT_ADDRESS() external view returns (address);
    function MAX_WITHDRAWAL_DATA_LENGTH() external view returns (uint256);
    function MAX_WITHDRAWAL_GAS_LIMIT() external view returns (uint64);
    function MESSAGE_VERSION() external view returns (uint16);
    function initialize() external;
    function l1CrossDomainMessenger() external view returns (address);
    function version() external view returns (string memory);

    function __constructor__(address _l1CrossDomainMessenger, address _l1mnt) external;
}
