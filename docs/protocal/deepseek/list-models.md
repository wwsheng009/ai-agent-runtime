# 列出模型

- 来源: https://api-docs.deepseek.com/zh-cn/api/list-models
- 抓取日期: 2026-04-24

`GET /models`

列出可用的模型列表，并提供相关模型的基本信息。请前往[模型 & 价格](<https://api-docs.deepseek.com/zh-cn/quick_start/pricing>)查看当前支持的模型列表

## Responses

  * 200

OK, 返回模型列表

  * application/json

  * Schema
  * Example (from schema)
  * Example

**

Schema

**

**object** stringrequired

**Possible values:** [`list`]

**

data

**

Model[]

required

  * Array [

**id** stringrequired

模型的标识符

**object** stringrequired

**Possible values:** [`model`]

对象的类型，其值为 `model`。

**owned_by** stringrequired

拥有该模型的组织。

  * ]

    
    
    {  
      "object": "list",  
      "data": [  
        {  
          "id": "string",  
          "object": "model",  
          "owned_by": "string"  
        }  
      ]  
    }  

    
    {  
      "object": "list",  
      "data": [  
        {  
          "id": "deepseek-v4-flash",  
          "object": "model",  
          "owned_by": "deepseek"  
        },  
        {  
          "id": "deepseek-v4-pro",  
          "object": "model",  
          "owned_by": "deepseek"  
        }  
      ]  
    }  

Loading...
