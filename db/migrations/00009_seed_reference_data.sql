-- +goose Up
-- =============================================================================
--  Baseline 9/9 · reference data: providers, mainnets, major assets
--
--  Idempotent (ON CONFLICT DO NOTHING) so it can be re-run safely. Adding a
--  chain or token later is a new migration in the same style.
--  Contract addresses are the well-known mainnet ones - verify before prod.
-- =============================================================================

INSERT INTO payment_providers (code, name, kind, adapter) VALUES
    ('tron_rpc',    'Tron node / TronGrid',   'node', 'tron'),
    ('evm_rpc',     'EVM JSON-RPC',           'node', 'evm'),
    ('bitcoin_rpc', 'Bitcoin Core / Electrs', 'node', 'bitcoin'),
    ('solana_rpc',  'Solana JSON-RPC',        'node', 'solana')
ON CONFLICT (code) DO NOTHING;

INSERT INTO networks (code, name, family, chain_id, native_symbol, native_decimals, required_confirmations, avg_block_time_ms,
                      address_regex, tx_hash_regex, explorer_tx_url, explorer_address_url) VALUES
    ('tron',     'Tron',           'tron',    NULL, 'TRX', 6,  19, 3000,
     '^T[1-9A-HJ-NP-Za-km-z]{33}$',                                    '^[0-9a-f]{64}$',
     'https://tronscan.org/#/transaction/{hash}',                      'https://tronscan.org/#/address/{address}'),
    ('ethereum', 'Ethereum',       'evm',     1,    'ETH', 18, 12, 12000,
     '^0x[0-9a-fA-F]{40}$',                                            '^0x[0-9a-f]{64}$',
     'https://etherscan.io/tx/{hash}',                                 'https://etherscan.io/address/{address}'),
    ('bsc',      'BNB Smart Chain','evm',     56,   'BNB', 18, 15, 3000,
     '^0x[0-9a-fA-F]{40}$',                                            '^0x[0-9a-f]{64}$',
     'https://bscscan.com/tx/{hash}',                                  'https://bscscan.com/address/{address}'),
    ('polygon',  'Polygon PoS',    'evm',     137,  'POL', 18, 64, 2000,
     '^0x[0-9a-fA-F]{40}$',                                            '^0x[0-9a-f]{64}$',
     'https://polygonscan.com/tx/{hash}',                              'https://polygonscan.com/address/{address}'),
    ('bitcoin',  'Bitcoin',        'bitcoin', NULL, 'BTC', 8,  3,  600000,
     '^(bc1[02-9ac-hj-np-z]{11,87}|[13][a-km-zA-HJ-NP-Z1-9]{25,34})$', '^[0-9a-f]{64}$',
     'https://mempool.space/tx/{hash}',                                'https://mempool.space/address/{address}'),
    ('solana',   'Solana',         'solana',  NULL, 'SOL', 9,  32, 400,
     '^[1-9A-HJ-NP-Za-km-z]{32,44}$',                                  '^[1-9A-HJ-NP-Za-km-z]{64,88}$',
     'https://solscan.io/tx/{hash}',                                   'https://solscan.io/account/{address}')
ON CONFLICT (code) DO NOTHING;

INSERT INTO provider_networks (provider_id, network_id, supports_fee_sponsorship)
SELECT p.id, n.id, (n.code = 'tron')
FROM payment_providers p
JOIN networks n ON (p.code, n.family) IN (('tron_rpc','tron'), ('evm_rpc','evm'), ('bitcoin_rpc','bitcoin'), ('solana_rpc','solana'))
ON CONFLICT DO NOTHING;

INSERT INTO assets (network_id, code, symbol, name, kind, token_standard, contract_address, decimals, is_stablecoin, min_deposit, min_withdrawal)
SELECT n.id, v.code, v.symbol, v.name, v.kind::asset_kind, v.std, v.contract, v.decimals, v.stable, v.min_dep, v.min_wd
FROM (VALUES
    -- Tron
    ('tron',     'TRX',          'TRX',  'Tron',                   'native', NULL,    NULL,                                           6,  FALSE, 1,       1),
    ('tron',     'USDT_TRON',    'USDT', 'Tether USD (TRC20)',     'token',  'trc20', 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t',           6,  TRUE,  1,       10),
    ('tron',     'USDC_TRON',    'USDC', 'USD Coin (TRC20)',       'token',  'trc20', 'TEkxiTehnzSmSe2XqrBj4w32RUN966rdz8',           6,  TRUE,  1,       10),
    -- Ethereum
    ('ethereum', 'ETH',          'ETH',  'Ether',                  'native', NULL,    NULL,                                           18, FALSE, 0.001,   0.005),
    ('ethereum', 'USDT_ETH',     'USDT', 'Tether USD (ERC20)',     'token',  'erc20', '0xdAC17F958D2ee523a2206206994597C13D831ec7',   6,  TRUE,  5,       20),
    ('ethereum', 'USDC_ETH',     'USDC', 'USD Coin (ERC20)',       'token',  'erc20', '0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48',   6,  TRUE,  5,       20),
    -- BNB Smart Chain
    ('bsc',      'BNB',          'BNB',  'BNB',                    'native', NULL,    NULL,                                           18, FALSE, 0.001,   0.005),
    ('bsc',      'USDT_BSC',     'USDT', 'Tether USD (BEP20)',     'token',  'bep20', '0x55d398326f99059fF775485246999027B3197955',   18, TRUE,  1,       10),
    -- Polygon
    ('polygon',  'POL',          'POL',  'Polygon Ecosystem Token','native', NULL,    NULL,                                           18, FALSE, 1,       1),
    ('polygon',  'USDT_POLYGON', 'USDT', 'Tether USD (Polygon)',   'token',  'erc20', '0xc2132D05D31c914a87C6611C10748AEb04B58e8F',   6,  TRUE,  1,       10),
    -- Bitcoin
    ('bitcoin',  'BTC',          'BTC',  'Bitcoin',                'native', NULL,    NULL,                                           8,  FALSE, 0.0001,  0.0005),
    -- Solana
    ('solana',   'SOL',          'SOL',  'Solana',                 'native', NULL,    NULL,                                           9,  FALSE, 0.01,    0.01),
    ('solana',   'USDC_SOL',     'USDC', 'USD Coin (SPL)',         'token',  'spl',   'EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v', 6,  TRUE,  1,       10),
    ('solana',   'USDT_SOL',     'USDT', 'Tether USD (SPL)',       'token',  'spl',   'Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB', 6,  TRUE,  1,       10)
) AS v(network, code, symbol, name, kind, std, contract, decimals, stable, min_dep, min_wd)
JOIN networks n ON n.code = v.network
ON CONFLICT DO NOTHING;

INSERT INTO provider_assets (provider_id, asset_id)
SELECT pn.provider_id, a.id FROM assets a JOIN provider_networks pn ON pn.network_id = a.network_id
ON CONFLICT DO NOTHING;

INSERT INTO chain_cursors (provider_id, network_id)
SELECT provider_id, network_id FROM provider_networks
ON CONFLICT DO NOTHING;

-- Platform-level ledger accounts, one per asset and type.
INSERT INTO ledger_accounts (organization_id, asset_id, type)
SELECT NULL, a.id, t
FROM assets a
CROSS JOIN unnest(ARRAY['platform_fee_revenue', 'platform_network_fees', 'platform_hot_wallet', 'platform_cold_wallet']::ledger_account_type[]) AS t
ON CONFLICT DO NOTHING;

-- Platform-wide default fee: 1% on deposits, 0.5% + network fee on withdrawals.
INSERT INTO fee_schedules (organization_id, asset_id, deposit_fee_bps, withdrawal_fee_bps, pass_network_fee)
VALUES (NULL, NULL, 100, 50, TRUE)
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM fee_schedules   WHERE organization_id IS NULL AND asset_id IS NULL;
DELETE FROM ledger_accounts WHERE organization_id IS NULL;
DELETE FROM chain_cursors;
DELETE FROM provider_assets;
DELETE FROM assets;
DELETE FROM provider_networks;
DELETE FROM networks;
DELETE FROM payment_providers;
