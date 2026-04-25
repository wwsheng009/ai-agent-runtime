# Anthropic API

- 来源: https://api-docs.deepseek.com/zh-cn/guides/anthropic_api
- 抓取日期: 2026-04-24

为了满足大家对 Anthropic API 生态的使用需求，我们的 API 新增了对 Anthropic API 格式的支持，其 `base_url` 为 `https://api.deepseek.com/anthropic`。

通过简单的配置，即可将 DeepSeek 的能力，接入到 Anthropic API 生态中。

* * *

## 将 DeepSeek 模型接入 Claude Code

请参考[接入 Coding Agent](<coding_agents.md>)

## 通过 Anthropic API 调用 DeepSeek 模型

  1. 安装 Anthropic SDK

    
    
    pip install anthropic  

  2. 配置环境变量

    
    
    export ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic  
    export ANTHROPIC_API_KEY=${YOUR_API_KEY}  

  3. 调用 API

    
    
    import anthropic  
      
    client = anthropic.Anthropic()  
      
    message = client.messages.create(  
        model="deepseek-v4-pro",  
        max_tokens=1000,  
        system="You are a helpful assistant.",  
        messages=[  
            {  
                "role": "user",  
                "content": [  
                    {  
                        "type": "text",  
                        "text": "Hi, how are you?"  
                    }  
                ]  
            }  
        ]  
    )  
    print(message.content)  

**注意** ：当您给 DeepSeek 的 Anthropic API 传入不支持的模型名时，API 后端会自动将其映射到 `deepseek-v4-flash` 模型。

* * *

## Anthropic API 兼容性细节

### HTTP Header

Field| Support Status  
---|---  
anthropic-beta| Ignored  
anthropic-version| Ignored  
x-api-key| Fully Supported  
  
### Simple Fields

Field| Support Status  
---|---  
model| Use DeepSeek Model Instead  
max_tokens| Fully Supported  
container| Ignored  
mcp_servers| Ignored  
metadata| Ignored  
service_tier| Ignored  
stop_sequences| Fully Supported  
stream| Fully Supported  
system| Fully Supported  
temperature| Fully Supported (range [0.0 ~ 2.0])  
thinking| Supported (`budget_tokens` is ignored)  
output_config| Only `effort` is supported  
top_k| Ignored  
top_p| Fully Supported  
  
### Tool Fields

#### tools

Field| Support Status  
---|---  
name| Fully Supported  
input_schema| Fully Supported  
description| Fully Supported  
cache_control| Ignored  
  
#### tool_choice

Value| Support Status  
---|---  
none| Fully Supported  
auto| Supported (`disable_parallel_tool_use` is ignored)  
any| Supported (`disable_parallel_tool_use` is ignored)  
tool| Supported (`disable_parallel_tool_use` is ignored)  
  
### Message Fields

Field| Variant| Sub-Field| Support Status  
---|---|---|---  
content |  string | | Fully Supported  
array, type="text"|  text |  Fully Supported   
cache_control |  Ignored   
citations |  Ignored   
array, type="image" | |  Not Supported   
array, type = "document" | |  Not Supported   
array, type = "search_result" | |  Not Supported   
array, type = "thinking" | |  Supported   
array, type="redacted_thinking" | |  Not Supported   
array, type = "tool_use" |  id |  Fully Supported   
input |  Fully Supported   
name |  Fully Supported   
cache_control |  Ignored   
array, type = "tool_result" |  tool_use_id |  Fully Supported   
content |  Fully Supported   
cache_control |  Ignored   
is_error |  Ignored   
array, type = "server_tool_use" | |  Not Supported   
array, type = "web_search_tool_result" | |  Not Supported   
array, type = "code_execution_tool_result" | |  Not Supported   
array, type = "mcp_tool_use" | |  Not Supported   
array, type = "mcp_tool_result" | |  Not Supported   
array, type = "container_upload" | |  Not Supported
