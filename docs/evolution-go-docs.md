### Documentação oficial da Evolution GO

Evolution Go

API WhatsApp de alta performance escrita em Go

    Documentation Index

    Fetch the complete documentation index at: https://docs.evolutionfoundation.com.br/llms.txt

    Use this file to discover all available pages before exploring further.

O Evolution Go é uma implementação de alta performance da API WhatsApp, escrita em Go. Construído com a biblioteca padrão do Go e práticas modernas de desenvolvimento, oferece uma solução robusta e eficiente para integração com WhatsApp utilizando a biblioteca whatsmeow.
​
Principais recursos

    Alta Performance — Construído em Go para máxima performance e uso mínimo de recursos
    API RESTful — Endpoints REST bem documentados e fáceis de usar
    Eventos em tempo real — Suporte a WebSocket para recebimento de mensagens em tempo real
    Armazenamento de mensagens — Integração opcional com PostgreSQL para persistência
    Suporte a mídia — Envio e recebimento de imagens, vídeos, áudios e documentos
    QR Code — Geração de QR Code para pareamento de dispositivos
    Docker — Configuração Docker pronta para uso
    Documentação Swagger — Documentação interativa auto-gerada
    Sistema de eventos — Suporte a webhooks, AMQP (RabbitMQ), NATS e WebSocket

​
Stack tecnológica
Tecnologia	Uso
Go 1.24+	Linguagem principal
net/http + ServeMux	Framework HTTP (biblioteca padrão)
whatsmeow	Biblioteca WhatsApp Web
PostgreSQL	Banco de dados (opcional)
Swagger/OpenAPI	Documentação da API
Docker	Containerização
RabbitMQ/AMQP	Fila de mensagens
MinIO/S3	Armazenamento de mídia

## Webhooks

https://docs.evolutionfoundation.com.br/evolution-go/webhooks

## Referência API

https://docs.evolutionfoundation.com.br/evolution-go/get-all-instances