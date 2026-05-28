import { Contract, JsonRpcProvider, getAddress } from "ethers";
import { createClient } from "redis";
import { pathToFileURL } from "node:url";

const ACTIVE_TOOLS_HASH_KEY = "skillfun:active_tools";
const ERC_8239_ABI = [
  "function ownerOf(uint256 tokenId) view returns (address)"
];

function requireEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) {
    throw new Error(`缺少必须的环境变量: ${name}`);
  }

  return value;
}

function parseNftId(rawNftId) {
  try {
    return BigInt(rawNftId);
  } catch {
    throw new Error(`NFT_ID 不是合法的整数: ${rawNftId}`);
  }
}

function parseMcpSchema(rawSchemaJson) {
  let parsed;

  try {
    parsed = JSON.parse(rawSchemaJson);
  } catch (error) {
    throw new Error(`MCP_SCHEMA_JSON 不是合法 JSON: ${error.message}`);
  }

  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("MCP_SCHEMA_JSON 必须是一个 JSON 对象");
  }

  if (typeof parsed.name !== "string" || parsed.name.trim() === "") {
    throw new Error("MCP_SCHEMA_JSON 缺少合法的 name 字段");
  }

  if (typeof parsed.description !== "string" || parsed.description.trim() === "") {
    throw new Error("MCP_SCHEMA_JSON 缺少合法的 description 字段");
  }

  if (
    !parsed.inputSchema ||
    typeof parsed.inputSchema !== "object" ||
    Array.isArray(parsed.inputSchema)
  ) {
    throw new Error("MCP_SCHEMA_JSON 缺少合法的 inputSchema 字段");
  }

  return parsed;
}

async function verifyOwnerOnChain({ rpcUrl, contractAddress, nftId, ownerAddress }) {
  const provider = new JsonRpcProvider(rpcUrl);

  try {
    const contract = new Contract(contractAddress, ERC_8239_ABI, provider);
    const onChainOwner = await contract.ownerOf(nftId);

    return getAddress(onChainOwner) === getAddress(ownerAddress);
  } finally {
    provider.destroy();
  }
}

async function publishSchemaToRedis({ redisUrl, nftId, schema }) {
  const redisClient = createClient({ url: redisUrl });

  redisClient.on("error", (error) => {
    console.error("Redis 客户端异常:", error);
  });

  await redisClient.connect();

  try {
    // 使用 NFT_ID 作为 Hash 字段，保证同一技能重复发布时可以原位覆盖。
    await redisClient.hSet(
      ACTIVE_TOOLS_HASH_KEY,
      nftId.toString(),
      JSON.stringify(schema)
    );
  } finally {
    await redisClient.quit();
  }
}

export async function main() {
  const nftId = parseNftId(requireEnv("NFT_ID"));
  const ownerAddress = getAddress(requireEnv("OWNER_ADDRESS"));
  const schema = parseMcpSchema(requireEnv("MCP_SCHEMA_JSON"));

  const rpcUrl = requireEnv("RPC_URL");
  const contractAddress = getAddress(requireEnv("ERC8239_CONTRACT_ADDRESS"));
  const redisUrl = requireEnv("REDIS_URL");

  const isOwnerMatched = await verifyOwnerOnChain({
    rpcUrl,
    contractAddress,
    nftId,
    ownerAddress
  });

  if (!isOwnerMatched) {
    throw new Error("链上 ownerOf(nftId) 与提交的 OWNER_ADDRESS 不一致");
  }

  await publishSchemaToRedis({
    redisUrl,
    nftId,
    schema
  });

  console.log("验证成功，技能已上线");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : error);
    process.exit(1);
  });
}
